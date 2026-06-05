package diffbench

import "encoding/binary"

// Decoders that reconstruct the next packed frame from (prev + payload). They
// exist to PROVE each encoding is lossless (the conformance bar: byte-identical
// frames). They are not on the hot path and may allocate freely.

func decodeFull(_ []byte, payload []byte) []byte {
	out := make([]byte, FrameBytes)
	copy(out, payload)
	return out
}

func decodeCellList(prev, payload []byte) []byte {
	out := make([]byte, FrameBytes)
	copy(out, prev)
	n := int(binary.LittleEndian.Uint16(payload[0:]))
	p := 2
	for k := 0; k < n; k++ {
		idx := int(binary.LittleEndian.Uint16(payload[p:]))
		p += 2
		copy(out[idx*CellBytes:idx*CellBytes+CellBytes], payload[p:p+CellBytes])
		p += CellBytes
	}
	return out
}

func decodeDirtyRows(prev, payload []byte) []byte {
	out := make([]byte, FrameBytes)
	copy(out, prev)
	mask := binary.LittleEndian.Uint32(payload[0:])
	p := 4
	for r := 0; r < Rows; r++ {
		if mask&(1<<uint(r)) == 0 {
			continue
		}
		base := r * RowBytes
		copy(out[base:base+RowBytes], payload[p:p+RowBytes])
		p += RowBytes
	}
	return out
}

// decodeRunList parses the v2 delta container: a 9-byte header (u8 flags,
// u32 epoch, u16 runCount, u16 reserved) then runCount runs. The keyframe bit
// (flags bit0) needs no special handling on reconstruct — a keyframe is just a
// run-list whose runs (here, one run of all 1920 cells) overwrite a baseline
// the host would have zeroed; against the bench's prev baseline the single
// full-cover run reproduces next exactly, so the same loop is lossless for both
// the steady-state delta and the keyframe form.
func decodeRunList(prev, payload []byte) []byte {
	out := make([]byte, FrameBytes)
	copy(out, prev)
	runs := int(binary.LittleEndian.Uint16(payload[5:])) // u16 runCount @ offset 5
	p := DeltaHeaderBytes
	for k := 0; k < runs; k++ {
		start := int(binary.LittleEndian.Uint16(payload[p:]))
		p += 2
		runLen := int(binary.LittleEndian.Uint16(payload[p:]))
		p += 2
		copy(out[start*CellBytes:(start+runLen)*CellBytes], payload[p:p+runLen*CellBytes])
		p += runLen * CellBytes
	}
	return out
}

// decodeSkipIdentical: an empty payload means "no change" (reuse prev).
func decodeSkipIdentical(prev, payload []byte) []byte {
	out := make([]byte, FrameBytes)
	if len(payload) == 0 {
		copy(out, prev)
		return out
	}
	copy(out, payload)
	return out
}

// decodeRunListOrKeyframe parses either the steady-state run-list delta or the
// keyframe form — both are the same v2 container (the keyframe is one run of all
// 1920 cells with flags bit0 set), so a single run-list parse reconstructs both.
func decodeRunListOrKeyframe(prev, payload []byte) []byte {
	return decodeRunList(prev, payload)
}
