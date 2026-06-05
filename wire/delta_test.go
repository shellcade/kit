package wire

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// randomFrame builds a packed FrameBytes buffer of canonical-zero cells. With
// p in [0,1] each cell gets a random rune (and occasionally a cp2/cp3) so the
// diff has work to do; PutCell enforces canonical zero so memcmp equality holds.
func randomFrame(rng *rand.Rand, fill float64) []byte {
	buf := make([]byte, FrameBytes)
	for i := 0; i < FrameCells; i++ {
		c := Cell{Rune: ' '}
		if rng.Float64() < fill {
			c.Rune = rune('A' + rng.Intn(26))
			if rng.Float64() < 0.2 {
				c.Cp2 = 0xFE0F // VS16
			}
			if rng.Float64() < 0.1 {
				c.Cp3 = 0x20E3 // keycap
			}
			if rng.Float64() < 0.3 {
				c.FGSet, c.FGR, c.FGG, c.FGB = true, uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256))
			}
		}
		PutCell(buf, i, c)
	}
	return buf
}

// applyVia builds a delta from base->next (with budget fallback) and applies it
// to a copy of base, returning the reconstructed frame.
func applyVia(t *testing.T, base, next []byte, epoch uint32) []byte {
	t.Helper()
	dst := make([]byte, MaxDeltaBytes)
	n := BuildFrameDelta(base, next, dst, epoch)
	if n >= KeyframeBytes {
		n = BuildKeyframe(next, dst, epoch)
	}
	payload := append([]byte(nil), dst[:n]...)
	if err := CheckFrameDelta(payload); err != nil {
		t.Fatalf("CheckFrameDelta rejected a self-built delta: %v", err)
	}
	out := append([]byte(nil), base...)
	if err := ApplyFrameDelta(out, payload); err != nil {
		t.Fatalf("ApplyFrameDelta: %v", err)
	}
	return out
}

func TestDeltaRoundTripRandomPairs(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	fills := []float64{0.0, 0.001, 0.01, 0.1, 0.5, 0.95, 1.0}
	for iter := 0; iter < 200; iter++ {
		base := randomFrame(rng, fills[rng.Intn(len(fills))])
		next := randomFrame(rng, fills[rng.Intn(len(fills))])
		out := applyVia(t, base, next, uint32(iter))
		if !bytes.Equal(out, next) {
			t.Fatalf("iter %d: apply(base, diff(base,next)) != next", iter)
		}
	}
}

func TestDeltaZeroChangeIsHeaderOnly(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	f := randomFrame(rng, 0.3)
	dst := make([]byte, MaxDeltaBytes)
	n := BuildFrameDelta(f, f, dst, 99)
	if n != DeltaHeaderBytes {
		t.Fatalf("no-change delta is %d bytes, want %d (header only)", n, DeltaHeaderBytes)
	}
	if rc := binary.LittleEndian.Uint16(dst[5:]); rc != 0 {
		t.Fatalf("no-change runCount = %d, want 0", rc)
	}
	if IsKeyframe(dst[:n]) {
		t.Fatal("no-change delta must not be a keyframe")
	}
	out := append([]byte(nil), f...)
	if err := ApplyFrameDelta(out, dst[:n]); err != nil || !bytes.Equal(out, f) {
		t.Fatalf("applying no-change delta changed the frame: err=%v equal=%v", err, bytes.Equal(out, f))
	}
}

func TestKeyframeFormExactSizeAndApply(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	next := randomFrame(rng, 0.4)
	dst := make([]byte, MaxDeltaBytes)
	n := BuildKeyframe(next, dst, 5)
	if n != KeyframeBytes {
		t.Fatalf("keyframe is %d bytes, want %d", n, KeyframeBytes)
	}
	if !IsKeyframe(dst[:n]) {
		t.Fatal("keyframe flag not set")
	}
	if rc := binary.LittleEndian.Uint16(dst[5:]); rc != 1 {
		t.Fatalf("keyframe runCount = %d, want 1", rc)
	}
	// A keyframe overwrites any baseline -> exactly next.
	garbage := randomFrame(rng, 1.0)
	if err := ApplyFrameDelta(garbage, dst[:n]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(garbage, next) {
		t.Fatal("keyframe did not overwrite the full baseline to next")
	}
}

func TestBudgetFallbackAtFullChange(t *testing.T) {
	// A full-change frame's run-list is one run of all 1920 cells == KeyframeBytes,
	// so the inclusive >= rule ships the keyframe form.
	base := bytes.Repeat([]byte{0}, FrameBytes)
	rng := rand.New(rand.NewSource(3))
	next := randomFrame(rng, 1.0) // every cell != base
	dst := make([]byte, MaxDeltaBytes)
	n := BuildFrameDelta(base, next, dst, 0)
	if n != KeyframeBytes {
		t.Fatalf("full-change one-run delta is %d bytes, want %d", n, KeyframeBytes)
	}
	if n < KeyframeBytes {
		t.Fatal("budget rule expects >= KeyframeBytes here")
	}
}

func TestCheckFrameDeltaRejections(t *testing.T) {
	good := make([]byte, MaxDeltaBytes)
	rng := rand.New(rand.NewSource(1))
	base := randomFrame(rng, 0.2)
	next := randomFrame(rng, 0.4)
	gn := BuildFrameDelta(base, next, good, 0)
	good = good[:gn]
	if err := CheckFrameDelta(good); err != nil {
		t.Fatalf("good delta rejected: %v", err)
	}

	cases := map[string]func() []byte{
		"short header": func() []byte { return good[:5] },
		"unknown flag bit": func() []byte {
			b := append([]byte(nil), good...)
			b[0] |= 0x02
			return b
		},
		"wrong rows": func() []byte {
			b := append([]byte(nil), good...)
			b[7] = 23
			return b
		},
		"wrong cols": func() []byte {
			b := append([]byte(nil), good...)
			b[8] = 79
			return b
		},
		"runCount too high": func() []byte {
			b := append([]byte(nil), good...)
			binary.LittleEndian.PutUint16(b[5:], 9999)
			return b
		},
		"trailing bytes": func() []byte { return append(append([]byte(nil), good...), 0, 0, 0) },
		"out-of-bounds run": func() []byte {
			b := make([]byte, DeltaHeaderBytes+RunHeaderBytes+CellBytes)
			putDeltaHeader(b, false, 0, 1)
			binary.LittleEndian.PutUint16(b[9:], FrameCells-0) // start at 1920
			binary.LittleEndian.PutUint16(b[11:], 1)           // len 1 -> 1921 > 1920
			return b
		},
		"overlapping runs": func() []byte {
			b := make([]byte, DeltaHeaderBytes+2*(RunHeaderBytes+CellBytes))
			putDeltaHeader(b, false, 0, 2)
			binary.LittleEndian.PutUint16(b[9:], 0)
			binary.LittleEndian.PutUint16(b[11:], 1)
			binary.LittleEndian.PutUint16(b[9+RunHeaderBytes+CellBytes:], 0) // start 0 again
			binary.LittleEndian.PutUint16(b[11+RunHeaderBytes+CellBytes:], 1)
			return b
		},
		"zero-length run": func() []byte {
			b := make([]byte, DeltaHeaderBytes+RunHeaderBytes)
			putDeltaHeader(b, false, 0, 1)
			binary.LittleEndian.PutUint16(b[9:], 0)
			binary.LittleEndian.PutUint16(b[11:], 0)
			return b
		},
	}
	for name, mk := range cases {
		if err := CheckFrameDelta(mk()); err == nil {
			t.Errorf("%s: CheckFrameDelta accepted a malformed container", name)
		}
	}
}

func FuzzCheckApplyFrameDelta(f *testing.F) {
	// Seed corpus: a valid delta, a keyframe, the no-change header, and junk.
	rng := rand.New(rand.NewSource(99))
	base := randomFrame(rng, 0.2)
	next := randomFrame(rng, 0.4)
	dst := make([]byte, MaxDeltaBytes)
	f.Add(dst[:BuildFrameDelta(base, next, dst, 0)])
	kf := make([]byte, MaxDeltaBytes)
	f.Add(kf[:BuildKeyframe(next, kf, 0)])
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, Rows, Cols}) // no-change header
	f.Add([]byte{})
	f.Add([]byte{1})

	f.Fuzz(func(t *testing.T, b []byte) {
		// CheckFrameDelta must never panic / OOB on arbitrary bytes.
		err := CheckFrameDelta(b)
		prev := make([]byte, FrameBytes)
		// ApplyFrameDelta must never panic / OOB; it succeeds iff Check passed.
		applyErr := ApplyFrameDelta(prev, b)
		if (err == nil) != (applyErr == nil) {
			t.Fatalf("Check/Apply disagree: check=%v apply=%v", err, applyErr)
		}
	})
}
