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
// Per run: u16 start index + u16 run length + (len * 16) packed cells. A header
// u16 carries the run count. This amortizes the per-cell index overhead of
// CELL-LIST across each contiguous span (the common case: a changed word, a
// changed row segment), at 4 bytes/run + 16 bytes/cell.
// Layout: u16 runCount, then runCount * (u16 start + u16 len + len*16 bytes).
func encodeRunList(prev, next, dst []byte) int {
	p := 2 // reserve run count
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
	binary.LittleEndian.PutUint16(dst[0:], uint16(runs))
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

// ---- (e) RUN-LIST + full-frame fallback -----------------------------------

// encodeRunListOrFull is RUN-LIST with the practical safety valve: if the delta
// would exceed the full frame size, ship the full frame instead (one leading
// byte distinguishes the two: 0 = full frame follows, 1 = run-list follows).
// This caps the worst case at FrameBytes+1 and is the encoding a real
// implementation would ship. It is benchmarked to show the cliff is bounded.
func encodeRunListOrFull(prev, next, dst []byte) int {
	// Encode run-list into the tail of dst (after the 1-byte tag), then decide.
	n := encodeRunList(prev, next, dst[1:])
	if n >= FrameBytes {
		dst[0] = 0
		return 1 + copy(dst[1:], next)
	}
	dst[0] = 1
	return 1 + n
}
