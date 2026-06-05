package diffbench

import "encoding/binary"

// The encoders below all operate on the PACKED wire representation of a frame
// (FrameBytes == 30720): prev and next are packed frames, dst is a reused
// scratch buffer the encoder writes the wire payload into, and the return value
// is the number of payload bytes produced. None of them allocate (dst is
// caller-owned and sized once), matching the SDK's allocation-free steady-state
// requirement under TinyGo's leaking GC.
//
// A cap big enough for any encoder's worst case (a full-change frame) is
// FrameBytes + a small framing overhead; bench buffers are sized to MaxEncoded.
const MaxEncoded = FrameBytes + FrameCells*2 + 8 // generous: worst-case cell-list

// DeltaHeaderBytes is the normative frame-delta container header (ABI v2,
// game-abi spec): u8 flags (bit0 = keyframe), u32 epoch, u16 runCount,
// u16 reserved = 0. Every send/identical in v2 carries the delta container, so
// the run-list encoders below stamp this header — the byte counts they report
// are exactly what a production guest puts on the wire.
const DeltaHeaderBytes = 9

// RunHeaderBytes is the per-run prefix inside a run-list payload: u16 startIndex
// + u16 runLen, followed by runLen*16 packed cells.
const RunHeaderBytes = 4

// flagKeyframe is header flags bit0: the payload is a self-contained keyframe
// (full frame), the bootstrap/full-frame form of the container.
const flagKeyframe = 0x01

// KeyframeBytes is the worst-case fallback size: the keyframe form is the
// 9-byte header (bit0 set) + ONE run covering all 1920 cells (u16 start=0,
// u16 len=1920) + the full 30720-byte packed grid = 30733 B. This is the v2
// worst case (the round-1 "RUN+FULL fallback" 1-byte tag is obsolete: in v2 the
// container IS the frame path and the keyframe form is the bootstrap/fallback).
const KeyframeBytes = DeltaHeaderBytes + RunHeaderBytes + FrameBytes // 30733

// putDeltaHeader writes the normative 9-byte container header into dst[0:9].
// epoch is modeled as 0 here (the byte COUNT is epoch-independent; the host is
// the sole epoch authority and the field is always present and fixed-width).
func putDeltaHeader(dst []byte, keyframe bool, runCount int) {
	if keyframe {
		dst[0] = flagKeyframe
	} else {
		dst[0] = 0
	}
	binary.LittleEndian.PutUint32(dst[1:], 0)             // u32 epoch (host-owned)
	binary.LittleEndian.PutUint16(dst[5:], uint16(runCount)) // u16 runCount
	binary.LittleEndian.PutUint16(dst[7:], 0)             // u16 reserved = 0
}

// cellEqual reports whether the 16-byte cell at offset o is identical in a and b.
func cellEqual(a, b []byte, o int) bool {
	// Compare as two uint64 loads: the packed cell is exactly 16 bytes and
	// 8-byte aligned within the frame (o is a multiple of 16).
	return binary.LittleEndian.Uint64(a[o:]) == binary.LittleEndian.Uint64(b[o:]) &&
		binary.LittleEndian.Uint64(a[o+8:]) == binary.LittleEndian.Uint64(b[o+8:])
}

// ---- (baseline) FULL ------------------------------------------------------

// encodeFull is the current baseline's WIRE FLOOR: ship the entire 30720-byte
// packed frame. Modeled as the copy into the send buffer — the minimum any
// full-frame ship must pay to hand the host a contiguous payload once the
// packed bytes already exist.
func encodeFull(_ /*prev*/, next, dst []byte) int {
	return copy(dst, next)
}

// encodeFullPack is the FAITHFUL current baseline: the guest holds an authoring
// Frame and composes the packed payload cell-by-cell via PutCell (internal/
// game/codec.go encodeFrame). That per-cell compose — 1920 PutCell calls every
// send — is the real steady-state CPU the baseline pays, and the honest floor
// the delta encoders' "scan + pack-only-dirty" must beat on CPU. We model the
// authoring->packed compose by re-packing each cell from next (reading the
// fields and writing them back is the same work PutCell does per cell).
func encodeFullPack(_ /*prev*/, next, dst []byte) int {
	for i := 0; i < FrameCells; i++ {
		o := i * CellBytes
		repackCell(next, dst, o)
	}
	return FrameBytes
}

// repackCell rebuilds the 16 packed bytes of one cell at offset o from src into
// dst, doing the same field-by-field work PutCell does (so the modeled compose
// cost matches the real codec, not a memcpy).
func repackCell(src, dst []byte, o int) {
	r := binary.LittleEndian.Uint32(src[o:])
	binary.LittleEndian.PutUint32(dst[o:], r)
	dst[o+4], dst[o+5], dst[o+6], dst[o+7] = src[o+4], src[o+5], src[o+6], src[o+7]
	dst[o+8], dst[o+9], dst[o+10], dst[o+11] = src[o+8], src[o+9], src[o+10], src[o+11]
	dst[o+12] = src[o+12]
	dst[o+13] = src[o+13]
	dst[o+14], dst[o+15] = 0, 0
}

// ---- (a) CELL-LIST --------------------------------------------------------

// encodeCellList emits, per changed cell, a u16 cell index followed by the
// 16-byte packed cell: 18 bytes/changed-cell, plus a u16 count header.
// Layout: u16 count, then count * (u16 index + 16 bytes).
func encodeCellList(prev, next, dst []byte) int {
	p := 2 // reserve the count header
	n := 0
	for i := 0; i < FrameCells; i++ {
		o := i * CellBytes
		if cellEqual(prev, next, o) {
			continue
		}
		binary.LittleEndian.PutUint16(dst[p:], uint16(i))
		p += 2
		copy(dst[p:], next[o:o+CellBytes])
		p += CellBytes
		n++
	}
	binary.LittleEndian.PutUint16(dst[0:], uint16(n))
	return p
}

// ---- (b) DIRTY-ROWS -------------------------------------------------------

// encodeDirtyRows emits a 24-bit row bitmap (3 bytes; one bit per row, padded
// in a u32 for alignment-free decode) followed by the full 1280-byte packed row
// for each dirty row. A row is dirty if any of its 80 cells changed.
// Layout: u32 rowBitmap (low 24 bits used), then (popcount) * 1280 bytes.
func encodeDirtyRows(prev, next, dst []byte) int {
	var mask uint32
	p := 4 // reserve the bitmap
	for r := 0; r < Rows; r++ {
		base := r * RowBytes
		dirty := false
		for c := 0; c < Cols; c++ {
			if !cellEqual(prev, next, base+c*CellBytes) {
				dirty = true
				break
			}
		}
		if !dirty {
			continue
		}
		mask |= 1 << uint(r)
		copy(dst[p:], next[base:base+RowBytes])
		p += RowBytes
	}
	binary.LittleEndian.PutUint32(dst[0:], mask)
	return p
}

// ---- (c) RUN-LIST ---------------------------------------------------------

// encodeRunList coalesces changed cells into runs of CONSECUTIVE changed cells.
// This is the v2 normative delta container: the 9-byte header (u8 flags,
// u32 epoch, u16 runCount, u16 reserved) followed by runCount runs, each
// {u16 startIndex, u16 runLen, runLen*16 packed cells}. It amortizes the
// per-cell index overhead of CELL-LIST across each contiguous span (a changed
// word, a row segment), at 4 bytes/run + 16 bytes/cell. A runCount==0 payload
// (the 9-byte header alone) is the legal "no change" delta.
// Layout: [9-byte header] then runCount * (u16 start + u16 len + len*16 bytes).
func encodeRunList(prev, next, dst []byte) int {
	p := DeltaHeaderBytes // reserve the container header
	runs := 0
	i := 0
	for i < FrameCells {
		if cellEqual(prev, next, i*CellBytes) {
			i++
			continue
		}
		start := i
		for i < FrameCells && !cellEqual(prev, next, i*CellBytes) {
			i++
		}
		runLen := i - start
		binary.LittleEndian.PutUint16(dst[p:], uint16(start))
		p += 2
		binary.LittleEndian.PutUint16(dst[p:], uint16(runLen))
		p += 2
		copy(dst[p:], next[start*CellBytes:(start+runLen)*CellBytes])
		p += runLen * CellBytes
		runs++
	}
	putDeltaHeader(dst, false, runs)
	return p
}

// ---- (d) SKIP-IDENTICAL ---------------------------------------------------

// encodeSkipIdentical is the degenerate compare-only encoding: if next equals
// prev, ship NOTHING (return 0 — the guest skips the send/identical host call
// entirely); otherwise fall back to the full frame. This measures the pure
// equality-compare cost (a ~30KB packed-frame memcmp) — the price of detecting
// "nothing changed", which is the single most common transition for static and
// turn-based games whose render-on-change still re-emits identical frames on
// stray wakes.
func encodeSkipIdentical(prev, next, dst []byte) int {
	if framesEqual(prev, next) {
		return 0
	}
	return copy(dst, next)
}

// framesEqual is the full packed-frame equality compare (8-byte stride).
func framesEqual(a, b []byte) bool {
	for o := 0; o < FrameBytes; o += 8 {
		if binary.LittleEndian.Uint64(a[o:]) != binary.LittleEndian.Uint64(b[o:]) {
			return false
		}
	}
	return true
}

// ---- (e) RUN-LIST + keyframe fallback (v2) ---------------------------------

// encodeKeyframe emits the v2 KEYFRAME FORM: the 9-byte container header with
// flags bit0 set + exactly ONE run covering all 1920 cells (u16 start=0,
// u16 len=1920) + the full 30720-byte packed grid. This is the bootstrap /
// full-frame mechanism of the v2 container and the worst-case fallback
// (KeyframeBytes = 30733). The round-1 "RUN+FULL fallback" 1-byte tag is gone:
// in v2 the delta container IS the frame path, and the keyframe is its
// self-contained full-frame member (accepted by the host regardless of epoch).
func encodeKeyframe(_ /*prev*/, next, dst []byte) int {
	putDeltaHeader(dst, true, 1)
	p := DeltaHeaderBytes
	binary.LittleEndian.PutUint16(dst[p:], 0)          // start index 0
	binary.LittleEndian.PutUint16(dst[p+2:], FrameCells) // run length 1920
	p += RunHeaderBytes
	p += copy(dst[p:], next)
	return p
}

// encodeRunListOrKeyframe is RUN-LIST with the v2 safety valve: if the delta
// payload would meet or exceed the keyframe form's size, ship the keyframe form
// instead. This caps the worst case at KeyframeBytes (30733) and is exactly the
// encoding a production guest ships — the run-list delta in the steady state,
// degrading to a self-contained keyframe on the full-change cliff. It is
// benchmarked to show the cliff is bounded.
func encodeRunListOrKeyframe(prev, next, dst []byte) int {
	n := encodeRunList(prev, next, dst)
	if n >= KeyframeBytes {
		return encodeKeyframe(prev, next, dst)
	}
	return n
}
