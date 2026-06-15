package sdk

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// checkpointHandler counts callbacks so a test can prove the room keeps running
// (still accepts input, still ticks) after a non-destructive Checkpoint.
type checkpointHandler struct {
	Base
	inputs int
}

func (h *checkpointHandler) OnInput(r Room, p Player, in Input) { h.inputs++ }

// Checkpoint runs fn ON the actor with the live Handler and does NOT dispose the
// room: the room keeps accepting input afterward, and fn sees the same handler
// instance the actor drives (no race).
func TestCheckpointNonDestructive(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
	h := &checkpointHandler{}
	ctl := NewRoomRuntime("cp1", h, cfg, Services{})
	p := mkPlayer("a")
	if err := ctl.Join(p); err != nil {
		t.Fatalf("join: %v", err)
	}

	var seen Handler
	if err := ctl.Checkpoint(func(hh Handler) error { seen = hh; return nil }); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if seen != h {
		t.Fatalf("Checkpoint handed a different handler than the runtime drives")
	}

	// The room MUST still be alive: not done, still accepting input.
	select {
	case <-ctl.Done():
		t.Fatal("Checkpoint disposed the room (must be non-destructive)")
	default:
	}
	ctl.Input(p, Input{Kind: InputRune, Rune: 'x'})
	// A second checkpoint observes the input the live room processed — proving the
	// room kept running on the same handler.
	deadline := time.After(time.Second)
	for {
		done := make(chan int, 1)
		_ = ctl.Checkpoint(func(hh Handler) error { done <- hh.(*checkpointHandler).inputs; return nil })
		if n := <-done; n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("input not processed after checkpoint; room not running")
		default:
		}
	}
}

// A fn error is returned to the caller and the room is NOT disposed (distinct
// from Hibernate, whose fn error leaves disposal to the caller too but whose
// success disposes).
func TestCheckpointErrorDoesNotDispose(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
	ctl := NewRoomRuntime("cp2", &checkpointHandler{}, cfg, Services{})
	want := errors.New("snapshot failed")
	if err := ctl.Checkpoint(func(Handler) error { return want }); !errors.Is(err, want) {
		t.Fatalf("Checkpoint err = %v, want %v", err, want)
	}
	select {
	case <-ctl.Done():
		t.Fatal("a failed checkpoint disposed the room")
	default:
	}
}

// A settled room returns ErrRoomClosed without calling fn.
func TestCheckpointOnSettledRoom(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
	ctl := NewRoomRuntime("cp3", endGame{}.NewRoom(cfg, Services{}), cfg, Services{})
	p := mkPlayer("a")
	_ = ctl.Join(p)
	ctl.Input(p, Input{Kind: InputRune, Rune: 'q'}) // ends the room
	<-ctl.Done()
	called := false
	if err := ctl.Checkpoint(func(Handler) error { called = true; return nil }); err == nil {
		t.Fatal("Checkpoint on a settled room should error")
	}
	if called {
		t.Fatal("Checkpoint called fn on a settled room")
	}
}

// Regression (review finding #5): Checkpoint/Hibernate called concurrently with
// the room ENDING must never deadlock. A command can land in the buffered cmds
// channel an instant before settle cancels the ctx and exits the loop, so the
// loop never processes it — a bare `<-reply` would block forever. awaitReply
// turns that into ErrRoomClosed. The test stresses the exact boundary under
// -race/-count and a per-call timeout guard FAILS instead of hanging.
func TestCheckpointHibernateDisposalRaceNoDeadlock(t *testing.T) {
	const rooms = 40
	var wg sync.WaitGroup
	for i := 0; i < rooms; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
			ctl := NewRoomRuntime("race", endGame{}.NewRoom(cfg, Services{}), cfg, Services{})
			p := mkPlayer("a")
			_ = ctl.Join(p)

			// End the room and hammer Checkpoint/Hibernate around the disposal so a
			// call frequently races the loop exit (the window awaitReply must close).
			go ctl.Input(p, Input{Kind: InputRune, Rune: 'q'}) // settle, concurrently

			for j := 0; j < 8; j++ {
				callWithDeadline(t, func() { _ = ctl.Checkpoint(func(Handler) error { return nil }) })
				callWithDeadline(t, func() { _ = ctl.Hibernate(func(Handler) error { return nil }) })
			}
			<-ctl.Done()
			// Post-disposal calls must also return promptly (not block on a reply the
			// dead loop will never send).
			callWithDeadline(t, func() { _ = ctl.Checkpoint(func(Handler) error { return nil }) })
			callWithDeadline(t, func() { _ = ctl.Hibernate(func(Handler) error { return nil }) })
		}()
	}
	wg.Wait()
}

// hibHandler opts into hibernation so explicit Hibernate freezes and disposes.
type hibHandler struct{ Base }

func (hibHandler) CanHibernate() bool { return true }

// Regression: a Hibernate the actor ACTUALLY PROCESSED (fn ran, returned nil)
// must return nil — never a false ErrRoomClosed. The actor's doHibernate cancels
// the room ctx as part of disposal; if that cancel becomes visible to awaitReply
// before the success reply does, awaitReply's ctx-done branch could find an empty
// reply chan and report ErrRoomClosed even though hibernation SUCCEEDED. That
// false negative makes HibernateAll count the room as not-frozen ("froze 0 rooms"
// / drain "hibernate: room is closed"). Each room is freshly created and frozen
// exactly once with no concurrent settle, so the ONLY ctx cancellation is the
// hibernate's own disposal — any ErrRoomClosed here is the bug. Stressed under
// -race/-count to widen the cancel-vs-reply window.
func TestHibernateSuccessNeverFalseClosed(t *testing.T) {
	const rooms = 200
	var wg sync.WaitGroup
	errs := make(chan error, rooms)
	for i := 0; i < rooms; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
			ctl := NewRoomRuntime("hibwin", &hibHandler{}, cfg, Services{})
			p := mkPlayer("a")
			if err := ctl.Join(p); err != nil {
				errs <- err
				return
			}
			ran := false
			err := ctl.Hibernate(func(Handler) error { ran = true; return nil })
			if !ran {
				errs <- errors.New("freeze fn never ran")
				return
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		// fn ran (room processed the command) but Hibernate reported an error:
		// the false ErrRoomClosed.
		t.Fatalf("Hibernate returned %v for a successfully-processed freeze", err)
	}
}

// callWithDeadline runs fn and fails the test if it does not return within a
// generous budget — surfacing a reply-deadlock as a failure, not a hung suite.
func callWithDeadline(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Checkpoint/Hibernate deadlocked racing room disposal (bare <-reply regression)")
	}
}
