package keyhold

import (
	"testing"
	"time"

	kit "github.com/shellcade/kit/v2"
)

func TestHeldBridgesRepeatGaps(t *testing.T) {
	tr := New(500 * time.Millisecond)
	t0 := time.Unix(0, 0)

	tr.Observe(kit.Input{Kind: kit.InputKey, Key: kit.KeyUp}, t0)
	if !tr.Held(kit.KeyUp, t0.Add(400*time.Millisecond)) {
		t.Fatal("held during the initial-repeat delay gap")
	}
	// repeats arrive; held continues
	tr.Observe(kit.Input{Kind: kit.InputKey, Key: kit.KeyUp}, t0.Add(450*time.Millisecond))
	if !tr.Held(kit.KeyUp, t0.Add(900*time.Millisecond)) {
		t.Fatal("held while repeats keep arriving")
	}
	// no events for > linger: released
	if tr.Held(kit.KeyUp, t0.Add(1*time.Second)) {
		t.Fatal("released after linger with no events")
	}
}

func TestRunesTrackedIndependently(t *testing.T) {
	tr := New(100 * time.Millisecond)
	t0 := time.Unix(0, 0)
	tr.Observe(kit.Input{Kind: kit.InputRune, Rune: ' '}, t0)
	if !tr.HeldRune(' ', t0.Add(50*time.Millisecond)) {
		t.Fatal("space held")
	}
	if tr.Held(kit.KeyUp, t0) {
		t.Fatal("unrelated key not held")
	}
	tr.ReleaseRune(' ')
	if tr.HeldRune(' ', t0.Add(1*time.Millisecond)) {
		t.Fatal("released rune forgotten immediately")
	}
}
