package diffbench

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestEmitGolden writes cross-language golden vectors for the v2 RUN-LIST delta
// encoder so an out-of-tree (Rust) reimplementation can be verified
// BYTE-IDENTICAL against this reference Go encoder. It is gated on
// DIFFBENCH_GOLDEN_DIR (the output directory) so a normal `go test` run does
// nothing; the verification harness sets it.
//
// One file per scenario (real captures + synthetics, including the v2 grapheme-
// churn synthetic that exercises cp2/cp3 non-zero). Each file is self-contained:
// it carries the reconstructed packed v2 input frames AND the reference encoder
// output, so the Rust side reproduces prev/next purely from the file (no need to
// re-implement the .fseq loader or the synthetic generators).
//
// File format (little-endian), magic "DGLD", version 2. The INPUT frame is
// stored as a changed-cell list against the previous frame (NOT the full 46080
// bytes) to keep committed fixtures small; this input encoding is deliberately
// independent of the encoder under test (a plain u16-index + 24-byte-cell list),
// so it is not circular. The Rust side reconstructs prev/next from it.
//
//	"DGLD" | u32 version=2 | u32 frameBytes=46080 | u32 frameCount
//	per frame:
//	  u32 changedCount | changedCount * (u16 cellIndex + 24 packed bytes)  (INPUT delta vs prev)
//	  u32 runlistLen   | runlistLen  bytes            (encodeRunList output)
//	  u32 fallbackLen  | fallbackLen bytes            (encodeRunListOrKeyframe)
//
// The KEYFRAME golden is NOT stored (it is the fully-derivable header + full
// next, 46KB/frame of pure bloat); the Rust side reconstructs the expected
// keyframe bytes from `next` and still asserts its encoder matches them exactly.
//
// prev for frame 0 is a BLANK frame (all-space cells), exactly as the bench
// diffs frame 0 against blankFrame(). The Rust side MUST seed prev with the same
// blank frame for frame 0, apply the input delta to get next, encode, compare,
// then carry next->prev.
func TestEmitGolden(t *testing.T) {
	dir := os.Getenv("DIFFBENCH_GOLDEN_DIR")
	if dir == "" {
		t.Skip("set DIFFBENCH_GOLDEN_DIR to emit cross-language golden vectors")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	real, err := realScenarios()
	if err != nil {
		t.Fatalf("loading real scenarios: %v", err)
	}
	scenarios := append(real, synthScenarios()...)

	dst := make([]byte, MaxEncoded)
	for _, s := range scenarios {
		var buf bytes.Buffer
		buf.WriteString("DGLD")
		writeU32(&buf, 2)
		writeU32(&buf, FrameBytes)
		writeU32(&buf, uint32(len(s.Frames)))

		prev := blankFrame()
		for _, next := range s.Frames {
			// INPUT delta vs prev: changedCount, then (u16 idx + 24-byte cell)*.
			var changed []int
			for i := 0; i < FrameCells; i++ {
				if !cellEqual(prev, next, i*CellBytes) {
					changed = append(changed, i)
				}
			}
			writeU32(&buf, uint32(len(changed)))
			for _, i := range changed {
				var idx [2]byte
				binary.LittleEndian.PutUint16(idx[:], uint16(i))
				buf.Write(idx[:])
				buf.Write(next[i*CellBytes : i*CellBytes+CellBytes])
			}

			n := encodeRunList(prev, next, dst)
			writeBlob(&buf, dst[:n])

			n = encodeRunListOrKeyframe(prev, next, dst)
			writeBlob(&buf, dst[:n])

			prev = next
		}

		path := filepath.Join(dir, s.Name+".dgld")
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d frames, %d bytes)", path, len(s.Frames), buf.Len())
	}
}

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func writeBlob(buf *bytes.Buffer, b []byte) {
	writeU32(buf, uint32(len(b)))
	buf.Write(b)
}
