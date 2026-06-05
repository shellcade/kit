package wire

import (
	"encoding/binary"
	"errors"
)

// The v2 frame-delta container (normative, ABI.md §4.5). Every send/identical
// carries this variable-length, little-endian, index-addressed run-list:
//
//	Header (9 bytes):
//	  u8  flags    bit0 = keyframe (1 = full-frame keyframe); all other bits MUST be zero
//	  u32 epoch    the host-issued epoch this delta is computed against (0 on a fresh instance)
//	  u16 runCount number of runs (keyframe: exactly 1; no-change: 0)
//	  u8  rows     grid geometry; MUST be 24 in v2
//	  u8  cols     grid geometry; MUST be 80 in v2
//	then runCount runs, each:
//	  u16 startIndex  first cell index (0..FrameCells-1, == row*Cols+col)
//	  u16 runLen      1..FrameCells, consecutive changed cells
//	  runLen × CellBytes  packed canonical-zero cells (PutCell output)
//
// These encoders/validators are index-addressed (mirroring PutCell/GetCell),
// never the appending Buf, so they are allocation-free over caller-owned
// scratch — the SDK's leaking-GC steady-state requirement.
const (
	// DeltaHeaderBytes is the fixed container header length.
	DeltaHeaderBytes = 9
	// RunHeaderBytes is the per-run prefix (u16 startIndex + u16 runLen).
	RunHeaderBytes = 4
	// KeyframeBytes is the keyframe form's exact size and the worst-case bound:
	// 9-byte header (bit0 set) + one run {start=0, len=FrameCells} + the full
	// 46080-byte packed grid = 46093. The SDK budget rule ships the keyframe
	// when an encoded delta would meet or exceed this (inclusive).
	KeyframeBytes = DeltaHeaderBytes + RunHeaderBytes + FrameBytes // 46093

	// FlagKeyframe is header flags bit0: the payload is a self-contained keyframe.
	FlagKeyframe = 0x01
	// flagsKnown is the set of assigned flag bits; any bit outside it is rejected.
	flagsKnown = FlagKeyframe
)

// MaxDeltaBytes is a scratch-buffer cap big enough for any delta this package
// emits: the run-list worst case never exceeds the keyframe form (the SDK caps
// it there), so KeyframeBytes is a sufficient and exact ceiling.
const MaxDeltaBytes = KeyframeBytes

// errMalformedDelta is the single non-fatal error every malformed-container
// path returns; the host logs "dropped malformed delta", drops, and bumps the
// slot epoch. Validators NEVER panic and NEVER read out of bounds.
var errMalformedDelta = errors.New("wire: malformed frame delta")

// cellEqual reports whether the 24-byte cell at byte offset o is identical in a
// and b. Under the canonical-zero rule cell equality IS a 24-byte memcmp, done
// here as three uint64 loads (the cell is 24 bytes; o is a multiple of 24).
func cellEqual(a, b []byte, o int) bool {
	return binary.LittleEndian.Uint64(a[o:]) == binary.LittleEndian.Uint64(b[o:]) &&
		binary.LittleEndian.Uint64(a[o+8:]) == binary.LittleEndian.Uint64(b[o+8:]) &&
		binary.LittleEndian.Uint64(a[o+16:]) == binary.LittleEndian.Uint64(b[o+16:])
}

// putDeltaHeader writes the 9-byte container header into dst[0:9].
func putDeltaHeader(dst []byte, keyframe bool, epoch uint32, runCount int) {
	if keyframe {
		dst[0] = FlagKeyframe
	} else {
		dst[0] = 0
	}
	binary.LittleEndian.PutUint32(dst[1:], epoch)            // u32 epoch
	binary.LittleEndian.PutUint16(dst[5:], uint16(runCount)) // u16 runCount
	dst[7] = Rows                                            // u8 rows  (24)
	dst[8] = Cols                                            // u8 cols  (80)
}

// BuildFrameDelta is the reference delta encoder: it diffs the packed frames
// base vs next into the caller-provided scratch dst (sized to at least
// MaxDeltaBytes) and returns the number of payload bytes. It coalesces changed
// cells into MAXIMAL runs of consecutive changed cells, greedy left-to-right
// (gap = 0): a single unchanged cell between two changed spans forces two runs.
// That determinism is what makes cross-implementation golden vectors
// byte-identical. epoch is stamped into the header (the slot's host-issued
// epoch). It allocates nothing.
//
// A runCount==0 result (the 9-byte header alone) is the canonical no-change
// delta. The keyframe form is NOT produced here — callers apply the budget rule
// (>= KeyframeBytes ⇒ BuildKeyframe) on the returned length.
func BuildFrameDelta(base, next, dst []byte, epoch uint32) int {
	p := DeltaHeaderBytes
	runs := 0
	i := 0
	for i < FrameCells {
		if cellEqual(base, next, i*CellBytes) {
			i++
			continue
		}
		start := i
		for i < FrameCells && !cellEqual(base, next, i*CellBytes) {
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
	putDeltaHeader(dst, false, epoch, runs)
	return p
}

// BuildKeyframe writes the keyframe form into dst: a 9-byte header (flags bit0
// set), one run {startIndex=0, runLen=FrameCells} and the full packed grid
// next. Returns KeyframeBytes (46093). It allocates nothing.
func BuildKeyframe(next, dst []byte, epoch uint32) int {
	putDeltaHeader(dst, true, epoch, 1)
	p := DeltaHeaderBytes
	binary.LittleEndian.PutUint16(dst[p:], 0)            // start index 0
	binary.LittleEndian.PutUint16(dst[p+2:], FrameCells) // run length 1920
	p += RunHeaderBytes
	p += copy(dst[p:], next)
	return p
}

// DeltaEpoch reads the epoch field from a container header without validating
// the rest. Callers use it to decide acceptance; it returns 0 on a short read.
func DeltaEpoch(b []byte) uint32 {
	if len(b) < DeltaHeaderBytes {
		return 0
	}
	return binary.LittleEndian.Uint32(b[1:])
}

// IsKeyframe reports whether the container's flags carry the keyframe bit. It
// returns false on a short read.
func IsKeyframe(b []byte) bool {
	return len(b) >= 1 && b[0]&FlagKeyframe != 0
}

// CheckFrameDelta validates a frame-delta container structurally without ever
// panicking or reading out of bounds (the drop-not-fatal contract). It checks:
//   - len >= 9 (header present)
//   - geometry bytes == (Rows, Cols) == (24, 80)
//   - no unknown flag bits set (only bit0 is assigned in v2 — rejected, not ignored)
//   - runCount consistent with body length: 9 + Σ(4 + runLen*24) == len(b)
//   - every run in-bounds: startIndex+runLen <= FrameCells
//   - runs strictly ascending and non-overlapping: start[i] >= start[i-1]+len[i-1]
//
// A short read degrades to errMalformedDelta. It does NOT require runs to be
// minimal, greedy, or true diffs — host acceptance is the envelope, so a
// hand-rolled guest's structurally valid container passes.
func CheckFrameDelta(b []byte) error {
	if len(b) < DeltaHeaderBytes {
		return errMalformedDelta
	}
	if b[0]&^flagsKnown != 0 {
		return errMalformedDelta // unknown flag bit set
	}
	if b[7] != Rows || b[8] != Cols {
		return errMalformedDelta // wrong geometry
	}
	runCount := int(binary.LittleEndian.Uint16(b[5:]))
	p := DeltaHeaderBytes
	prevEnd := 0 // first allowed startIndex
	for k := 0; k < runCount; k++ {
		if p+RunHeaderBytes > len(b) {
			return errMalformedDelta
		}
		start := int(binary.LittleEndian.Uint16(b[p:]))
		runLen := int(binary.LittleEndian.Uint16(b[p+2:]))
		if runLen == 0 || start+runLen > FrameCells {
			return errMalformedDelta // empty or out-of-bounds run
		}
		if start < prevEnd {
			return errMalformedDelta // not strictly ascending / overlapping
		}
		p += RunHeaderBytes
		body := runLen * CellBytes
		if p+body > len(b) {
			return errMalformedDelta
		}
		p += body
		prevEnd = start + runLen
	}
	if p != len(b) {
		return errMalformedDelta // trailing bytes / length disagrees with runCount
	}
	return nil
}

// ApplyFrameDelta applies a (validated) delta in place to prev, a FrameBytes
// baseline buffer: each run's cells are copied into prev at startIndex*24, so a
// keyframe (one full-cover run) overwrites all 1920 cells. It re-validates
// structurally (so a caller may apply untrusted bytes safely) and returns
// errMalformedDelta on any malformation, never panicking, never reading OOB,
// and never partially mutating prev on a malformed container (it validates
// fully first). It allocates nothing.
func ApplyFrameDelta(prev, delta []byte) error {
	if len(prev) != FrameBytes {
		return errMalformedDelta
	}
	if err := CheckFrameDelta(delta); err != nil {
		return err
	}
	runCount := int(binary.LittleEndian.Uint16(delta[5:]))
	p := DeltaHeaderBytes
	for k := 0; k < runCount; k++ {
		start := int(binary.LittleEndian.Uint16(delta[p:]))
		runLen := int(binary.LittleEndian.Uint16(delta[p+2:]))
		p += RunHeaderBytes
		body := runLen * CellBytes
		copy(prev[start*CellBytes:start*CellBytes+body], delta[p:p+body])
		p += body
	}
	return nil
}
