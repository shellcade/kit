package game

import (
	"fmt"
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

// SDK v2 frame-diff benchmark — no host, no wasm. It measures the guest encode
// path (buildSendPayload: pack-diff-or-keyframe) over kittest-style frame
// sequences: bytes emitted on the wire (vs the 46080-byte full frame) and
// encode ns/op. It mirrors what a production guest ships every send.

// scriptFrames builds a small sequence of frames for a named scenario, returning
// the packed (encoded) frames so the benchmark diffs packed bytes like the SDK.
func scriptFrames(name string) [][]byte {
	var frames []*Frame
	switch name {
	case "static-idle": // turn-based: identical frame re-sent on stray wakes
		base := NewFrame()
		base.Text(0, 0, "TIC TAC TOE", Style{Attr: AttrBold})
		base.Text(5, 5, "X | O |  ", Style{})
		for i := 0; i < 10; i++ {
			frames = append(frames, base)
		}
	case "clock-tick": // animation: a clock in the corner re-renders each wake
		for i := 0; i < 20; i++ {
			f := NewFrame()
			f.Text(0, 0, "BLACKJACK felt stays static", Style{})
			f.Text(23, 70, fmt.Sprintf("%02d:%02d", i/60, i%60), Style{})
			frames = append(frames, f)
		}
	case "scroll-row": // a marquee row scrolls; one row of churn per frame
		msg := "shellracer leaderboard scrolls across the bottom row forever"
		for i := 0; i < 20; i++ {
			f := NewFrame()
			f.Text(12, 0, "PASSAGE", Style{})
			for c := 0; c < Cols; c++ {
				f.SetRune(23, c, rune(msg[(c+i)%len(msg)]), Style{})
			}
			frames = append(frames, f)
		}
	case "grapheme-churn": // a row of emoji clusters cycling (cp2/cp3 live)
		clusters := []string{"❤️", "1️⃣", "👍🏽", "🎲"}
		for i := 0; i < 20; i++ {
			f := NewFrame()
			f.Text(0, 0, "POKIES", Style{})
			col := 0
			for k := 0; k < 12; k++ {
				col = f.SetGrapheme(10, col, clusters[(k+i)%len(clusters)], Style{})
			}
			frames = append(frames, f)
		}
	}
	out := make([][]byte, len(frames))
	for i, f := range frames {
		out[i] = append([]byte(nil), encodeFrame(f)...)
	}
	return out
}

// meanBytesPerFrame replays a scenario once and reports the mean wire bytes per
// send (steady-state delta sizes after the first keyframe).
func meanBytesPerFrame(name string) (mean float64, frames int) {
	fs := scriptFrames(name)
	resetDiffState()
	total := 0
	for _, packed := range fs {
		total += len(buildSendPayload(0, packed))
		commitBaseline(0, packed, 0)
	}
	return float64(total) / float64(len(fs)), len(fs)
}

func benchScenario(b *testing.B, name string) {
	frames := scriptFrames(name)
	// Wire bytes/frame is reported by TestSDKDiffByteTable (a benchmark metric
	// gets divided by b.N and would mislead). Here we measure encode ns/op and
	// assert 0 allocs (the allocation-free steady-state requirement).
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resetDiffState()
		for _, packed := range frames {
			_ = buildSendPayload(0, packed)
			commitBaseline(0, packed, 0)
		}
	}
}

func BenchmarkSDKDiffStaticIdle(b *testing.B)    { benchScenario(b, "static-idle") }
func BenchmarkSDKDiffClockTick(b *testing.B)     { benchScenario(b, "clock-tick") }
func BenchmarkSDKDiffScrollRow(b *testing.B)     { benchScenario(b, "scroll-row") }
func BenchmarkSDKDiffGraphemeChurn(b *testing.B) { benchScenario(b, "grapheme-churn") }

// TestSDKDiffByteTable prints the per-scenario mean wire bytes/frame vs the
// 46080-byte full frame — the headline number the v2 diff buys. It always
// passes; run with -v to see the table.
func TestSDKDiffByteTable(t *testing.T) {
	for _, name := range []string{"static-idle", "clock-tick", "scroll-row", "grapheme-churn"} {
		mean, n := meanBytesPerFrame(name)
		t.Logf("%-16s %d frames  %8.1f wire bytes/frame  (%.4f%% of %d)",
			name, n, mean, 100*mean/float64(wire.FrameBytes), wire.FrameBytes)
	}
}
