// Package keyhold derives "key held" state from terminal auto-repeat — the
// closest a terminal game can get to press/release semantics.
//
// Terminals (and SSH) deliver only discrete key events: there is NO key-up,
// and holding a key produces an initial press, a delay (~250–600ms depending
// on the user's OS settings), then repeats (~15–35Hz). A Tracker treats a key
// as held from its first press until Linger elapses with no further events,
// which bridges both the initial-delay gap and the inter-repeat gaps.
//
// Usage (asteroids-style thrust):
//
//	tracker := keyhold.New(0) // default linger
//
//	func (rm *room) OnInput(r kit.Room, p kit.Player, in kit.Input) {
//	    rm.tracker.Observe(in, r.Now())
//	}
//	func (rm *room) OnWake(r kit.Room) {
//	    if rm.tracker.Held(kit.KeyUp, r.Now()) {
//	        rm.thrust(dt) // integrate while held — do NOT count events
//	    }
//	}
//
// Honest physics: integrate against elapsed time while Held reports true,
// never per-event (event rate is the user's terminal repeat rate). The cost of
// the trick is release latency: a key reads held for up to Linger after the
// user lets go. Tune Linger per feel — lower is snappier release but risks
// flicker on slow repeat rates; the default rides above common initial-delay
// settings.
package keyhold

import (
	"time"

	kit "github.com/shellcade/kit/v2"
)

// DefaultLinger comfortably exceeds common terminal initial-repeat delays.
const DefaultLinger = 550 * time.Millisecond

// Tracker derives held state for named keys and printable runes.
type Tracker struct {
	linger time.Duration
	keys   map[kit.Key]time.Time
	runes  map[rune]time.Time
}

// New returns a Tracker. linger <= 0 selects DefaultLinger.
func New(linger time.Duration) *Tracker {
	if linger <= 0 {
		linger = DefaultLinger
	}
	return &Tracker{
		linger: linger,
		keys:   map[kit.Key]time.Time{},
		runes:  map[rune]time.Time{},
	}
}

// Observe records one input event at the room time it arrived. Call it from
// OnInput with r.Now().
func (t *Tracker) Observe(in kit.Input, now time.Time) {
	if in.Kind == kit.InputKey {
		t.keys[in.Key] = now
	} else {
		t.runes[in.Rune] = now
	}
}

// Held reports whether a named key is currently held at room time now.
func (t *Tracker) Held(k kit.Key, now time.Time) bool {
	last, ok := t.keys[k]
	return ok && now.Sub(last) <= t.linger
}

// HeldRune reports whether a printable rune is currently held at room time now.
func (t *Tracker) HeldRune(r rune, now time.Time) bool {
	last, ok := t.runes[r]
	return ok && now.Sub(last) <= t.linger
}

// Release forgets a key immediately (e.g. consume a one-shot trigger).
func (t *Tracker) Release(k kit.Key) { delete(t.keys, k) }

// ReleaseRune forgets a rune immediately.
func (t *Tracker) ReleaseRune(r rune) { delete(t.runes, r) }
