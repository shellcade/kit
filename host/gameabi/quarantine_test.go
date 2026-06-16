package gameabi

import (
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// TestQuarantineWindow pins the counting rule: only faults inside the window
// count toward the threshold, and Restore returns the game with a clean slate.
func TestQuarantineWindow(t *testing.T) {
	g := loadFixture(t, Options{})
	reg := sdk.NewRegistry()
	reg.MustAdd(g)
	q := NewQuarantine(reg, 2, time.Minute, quietLog())
	clock := time.Unix(1_700_000_000, 0)
	q.now = func() time.Time { return clock }

	q.RecordFault("fixture")
	clock = clock.Add(2 * time.Minute) // first fault ages out of the window
	q.RecordFault("fixture")
	if _, ok := reg.Get("fixture"); !ok {
		t.Fatal("quarantined on faults outside one window")
	}

	clock = clock.Add(time.Second)
	q.RecordFault("fixture") // second in-window fault: threshold reached
	if _, ok := reg.Get("fixture"); ok {
		t.Fatal("game still in roster after threshold faults in window")
	}
	if qs := q.Quarantined(); len(qs) != 1 || qs[0] != "fixture" {
		t.Fatalf("Quarantined() = %v, want [fixture]", qs)
	}

	if err := q.Restore("fixture"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := reg.Get("fixture"); !ok {
		t.Fatal("restored game missing from roster")
	}
	if len(q.Quarantined()) != 0 {
		t.Fatal("Quarantined() not empty after restore")
	}
	if err := q.Restore("fixture"); err == nil {
		t.Fatal("double restore accepted")
	}
}

// TestQuarantineOnQuarantineHook pins the catalog hook (3.4): OnQuarantine fires
// exactly ONCE with the slug when the watchdog pulls the game — and not again on
// later faults from rooms still running the quarantined game.
func TestQuarantineOnQuarantineHook(t *testing.T) {
	g := loadFixture(t, Options{})
	reg := sdk.NewRegistry()
	reg.MustAdd(g)
	q := NewQuarantine(reg, 2, time.Minute, quietLog())
	var got []string
	q.OnQuarantine = func(slug string) { got = append(got, slug) }

	q.RecordFault("fixture")
	if len(got) != 0 {
		t.Fatalf("hook fired before threshold: %v", got)
	}
	q.RecordFault("fixture") // threshold reached -> quarantined
	if len(got) != 1 || got[0] != "fixture" {
		t.Fatalf("OnQuarantine = %v, want one [fixture]", got)
	}
	// A later fault (from a room still running the quarantined game) must NOT
	// re-fire the hook — the game is already removed.
	q.RecordFault("fixture")
	if len(got) != 1 {
		t.Fatalf("OnQuarantine fired again after removal: %v", got)
	}
}

// TestQuarantineLive is the end-to-end story: two rooms of a wasm game each
// fault, the watchdog pulls the game from the roster, a third room of the
// same game keeps running (removal spares running rooms), and an admin
// restore brings the game back.
func TestQuarantineLive(t *testing.T) {
	reg := sdk.NewRegistry()
	q := NewQuarantine(reg, 2, time.Minute, quietLog())
	g, err := LoadGame(fixturePath, Options{OnFault: q.RecordFault})
	if err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	reg.MustAdd(g)

	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	survivor := newLiveRoom(t, g, cfg)
	if err := survivor.Join(p2); err != nil {
		t.Fatalf("join survivor: %v", err)
	}
	framesSurvivor := survivor.Frames(p2)

	for i := 0; i < 2; i++ {
		ctl := newLiveRoom(t, g, cfg)
		if err := ctl.Join(p1); err != nil {
			t.Fatalf("join faulting room %d: %v", i, err)
		}
		ctl.Input(p1, runeIn('p'))
		select {
		case <-ctl.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("faulting room %d did not settle", i)
		}
	}

	waitFor(t, "quarantine removal", func() bool {
		_, ok := reg.Get("fixture")
		return !ok
	})

	// The already-running room is spared.
	drain(framesSurvivor)
	survivor.Input(p2, runeIn('x'))
	select {
	case _, ok := <-framesSurvivor:
		if !ok {
			t.Fatal("survivor frame stream closed by quarantine")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("survivor room unresponsive after quarantine")
	}

	if err := q.Restore("fixture"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := reg.Get("fixture"); !ok {
		t.Fatal("restore did not return the game to the roster")
	}
}
