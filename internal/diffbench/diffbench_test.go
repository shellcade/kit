package diffbench

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"testing"
)

// encoder bundles an encode fn (hot path, into a reused buffer) and its decode
// fn (correctness only).
type encoder struct {
	name   string
	encode func(prev, next, dst []byte) int
	decode func(prev, payload []byte) []byte
}

func encoders() []encoder {
	return []encoder{
		{"FULL-baseline", encodeFull, decodeFull},
		{"FULL-pack", encodeFullPack, decodeFull},
		{"CELL-LIST", encodeCellList, decodeCellList},
		{"DIRTY-ROWS", encodeDirtyRows, decodeDirtyRows},
		{"RUN-LIST", encodeRunList, decodeRunList},
		{"SKIP-IDENTICAL", encodeSkipIdentical, decodeSkipIdentical},
		{"RUN+KEYFRAME-fallback", encodeRunListOrKeyframe, decodeRunListOrKeyframe},
	}
}

// allScenarios = real captures (if present) + labelled synthetics.
func allScenarios(t testing.TB) []*Sequence {
	real, err := realScenarios()
	if err != nil {
		t.Fatalf("loading real scenarios: %v", err)
	}
	return append(real, synthScenarios()...)
}

// TestEncodersLossless proves every encoding reconstructs byte-identical frames
// across every scenario (the conformance bar). A frame-diff scheme that loses a
// single byte is disqualified regardless of its size win.
func TestEncodersLossless(t *testing.T) {
	dst := make([]byte, MaxEncoded)
	for _, s := range allScenarios(t) {
		prev := blankFrame()
		for fi, next := range s.Frames {
			for _, e := range encoders() {
				n := e.encode(prev, next, dst)
				got := e.decode(prev, dst[:n])
				if !bytes.Equal(got, next) {
					t.Fatalf("%s / %s frame %d: decode mismatch", s.Name, e.name, fi)
				}
			}
			prev = next
		}
	}
}

// meanBytes returns the mean payload bytes/send for an encoding over a sequence
// (frame 0 diffed against a blank frame, mirroring the first real send after a
// Clear). For SKIP-IDENTICAL a 0-byte "send" counts as 0 (no host call).
func meanBytes(e encoder, s *Sequence, dst []byte) float64 {
	prev := blankFrame()
	total := 0
	for _, next := range s.Frames {
		total += e.encode(prev, next, dst)
		prev = next
	}
	return float64(total) / float64(len(s.Frames))
}

// BenchmarkEncode is the matrix: for each scenario x encoding it measures
// encode ns/op and allocs/op (must be 0). bytes/send is reported as a custom
// metric (the headline) plus surfaced in TestReport. Each op encodes the WHOLE
// sequence once (so ns/op is per-sequence; divide by frame count for per-frame,
// reported in the table).
func BenchmarkEncode(b *testing.B) {
	scenarios := allScenarios(b)
	dst := make([]byte, MaxEncoded)
	for _, s := range scenarios {
		for _, e := range encoders() {
			e := e
			s := s
			b.Run(s.Name+"/"+e.name, func(b *testing.B) {
				// Warm: compute mean bytes once for the custom metric.
				mb := meanBytes(e, s, dst)
				// Pre-allocate the initial prev frame OUTSIDE the timer so
				// allocs/op reflects only the encoder (the steady-state guest
				// reuses one prev buffer too).
				blank := blankFrame()
				b.ReportAllocs()
				b.ResetTimer()
				var sink int
				for i := 0; i < b.N; i++ {
					prev := blank
					for _, next := range s.Frames {
						sink += e.encode(prev, next, dst)
						prev = next
					}
				}
				_ = sink
				b.ReportMetric(mb, "B/send")
				b.ReportMetric(float64(len(s.Frames)), "frames")
			})
		}
	}
}

// --- summary report ----------------------------------------------------------

// TestReport prints a dense scenario x encoding table (mean B/send, % of the
// 30720 baseline) plus the average changed-cell density per scenario. Run with:
//
//	go test -run TestReport -v ./internal/diffbench/
func TestReport(t *testing.T) {
	if os.Getenv("DIFFBENCH_REPORT") == "" && !testing.Verbose() {
		t.Skip("set -v or DIFFBENCH_REPORT=1 to print the report")
	}
	scenarios := allScenarios(t)
	encs := encoders()
	dst := make([]byte, MaxEncoded)

	var buf bytes.Buffer
	fmt.Fprintln(&buf)
	fmt.Fprintf(&buf, "Frame-delta wire size — mean bytes/send (baseline full frame = %d B)\n\n", FrameBytes)

	// header
	fmt.Fprintf(&buf, "%-28s %8s", "scenario (frames, Δcells)", "")
	for _, e := range encs {
		fmt.Fprintf(&buf, " %18s", e.name)
	}
	fmt.Fprintln(&buf)

	for _, s := range scenarios {
		label := fmt.Sprintf("%s (%d, %.0f)", s.Name, len(s.Frames), avgChangedCells(s))
		fmt.Fprintf(&buf, "%-37s", label)
		for _, e := range encs {
			mb := meanBytes(e, s, dst)
			pct := 100 * mb / float64(FrameBytes)
			fmt.Fprintf(&buf, " %10.0f(%4.0f%%)", mb, pct)
		}
		fmt.Fprintln(&buf)
	}
	fmt.Fprintln(&buf)
	t.Log(buf.String())
}

// TestWorstCase isolates and prints the full-change cliff numbers (frame N+1
// shares no cell with N): the delta encodings' worst case vs the baseline.
func TestWorstCase(t *testing.T) {
	if !testing.Verbose() {
		t.Skip("set -v to print worst-case numbers")
	}
	s := synthWorstCase()
	dst := make([]byte, MaxEncoded)
	type row struct {
		name string
		b    float64
	}
	var rows []row
	for _, e := range encoders() {
		rows = append(rows, row{e.name, meanBytes(e, s, dst)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].b < rows[j].b })
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\nWORST CASE (full-change, every cell differs):\n")
	for _, r := range rows {
		fmt.Fprintf(&buf, "  %-20s %8.0f B/send  (%.1fx baseline)\n", r.name, r.b, r.b/FrameBytes)
	}
	t.Log(buf.String())
}
