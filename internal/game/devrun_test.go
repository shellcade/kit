//go:build !wasip1 && !tinygo.wasm

package game

import (
	"strings"
	"testing"
	"time"
)

func TestSeedEpochDeterministic(t *testing.T) {
	// Same seed -> same epoch; different seeds -> (generally) different epochs.
	if seedEpoch(42) != seedEpoch(42) {
		t.Fatal("seedEpoch is not a pure function of the seed")
	}
	if seedEpoch(1) == seedEpoch(2) {
		t.Fatal("distinct small seeds collapsed to the same epoch")
	}
	// A negative seed must still yield a sane (post-base, non-panicking) time.
	if got := seedEpoch(-1); got.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("negative seed produced a pre-base epoch: %v", got)
	}
}

func TestVirtualClockAdvancesOneBeatPerWake(t *testing.T) {
	beat := 50 * time.Millisecond
	r := &nativeRoom{virtual: true, beat: beat, clock: seedEpoch(7)}
	start := r.Now()

	// The run loop advances the clock by exactly one beat immediately before
	// each OnWake and at no other time. Reads between wakes are stable.
	if r.Now() != start {
		t.Fatal("Now() changed without a wake")
	}
	for i := 1; i <= 4; i++ {
		r.clock = r.clock.Add(r.beat) // mirrors the wake step in Main
		want := start.Add(time.Duration(i) * beat)
		got := r.Now()
		if got != want {
			t.Fatalf("after %d wakes: got %v, want %v", i, got, want)
		}
		// Multiple reads inside the same wake return the same instant.
		if r.Now() != got {
			t.Fatalf("Now() not stable within a wake at step %d", i)
		}
	}
}

func TestWallClockWithoutSeed(t *testing.T) {
	r := &nativeRoom{} // virtual=false: wall clock
	before := time.Now()
	got := r.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("wall-clock Now() %v outside [%v, %v]", got, before, after)
	}
}

func TestFrameToANSIBurstsGraphemeContiguously(t *testing.T) {
	f := NewFrame()
	f.SetGrapheme(0, 0, "1️⃣", Style{}) // '1' + VS16 + keycap U+20E3
	out := frameToANSI(f)
	want := "1️⃣" // base, cp2, cp3 contiguous
	if !strings.Contains(out, want) {
		t.Fatalf("grapheme not burst contiguously; row0 render lacks %q", want)
	}
	// A wide grapheme bursts base+cp2 contiguously and the Cont cell emits
	// nothing extra for its position.
	g := NewFrame()
	g.SetGraphemeWide(0, 0, "❤️", Style{})
	if !strings.Contains(frameToANSI(g), "❤️") {
		t.Fatal("wide grapheme lead not burst contiguously")
	}
}
