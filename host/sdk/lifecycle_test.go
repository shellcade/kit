package sdk

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// lcHandler counts callbacks; Base supplies no-ops.
type lcHandler struct {
	Base
	ticks int
}

func (h *lcHandler) OnTick(r Room, now time.Time) { h.ticks++ }

// CanHibernate marks the stub hibernation-capable (the wasm handler posture),
// so Hibernatable() reflects only the lifecycle wiring under test.
func (h *lcHandler) CanHibernate() bool { return true }

func lcPlayer() Player {
	return Player{AccountID: "a1", Handle: "ada", Kind: KindMember, Conn: "c1"}
}

// An ephemeral room survives the grace window (a rejoin finds it alive) and
// ENDS at expiry — no hibernation, no snapshot fn ever wired.
func TestEphemeralEndsAfterGrace(t *testing.T) {
	h := &lcHandler{}
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 4, MinPlayers: 1, Seed: 1, SeedSet: true, Lifecycle: LifecycleEphemeral}
	ctl := NewRoomRuntime("eph-1", h, cfg, Services{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, WithAbandonGrace(40*time.Millisecond))
	defer ctl.Close()

	p := lcPlayer()
	if err := ctl.Join(p); err != nil {
		t.Fatalf("join: %v", err)
	}
	ctl.Leave(p)

	// Within the grace: the room is alive and a rejoin works.
	time.Sleep(10 * time.Millisecond)
	if err := ctl.Join(p); err != nil {
		t.Fatalf("rejoin within grace: %v", err)
	}
	ctl.Leave(p)

	// Past the grace: the room ends (Done closes) — and was never hibernated.
	select {
	case <-ctl.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ephemeral room did not end after the abandon grace")
	}
	if ctl.Hibernatable() {
		t.Fatal("ephemeral room reports hibernatable")
	}
}

// A resident room ignores abandonment entirely: empty past the grace, it is
// still alive and still ticking.
func TestResidentIgnoresAbandonment(t *testing.T) {
	h := &lcHandler{}
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 4, MinPlayers: 1, Seed: 1, SeedSet: true, Lifecycle: LifecycleResident}
	froze := false
	ctl := NewRoomRuntime("resident-test", h, cfg, Services{Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		WithAbandonHibernate(func(Handler) error { froze = true; return nil }, 30*time.Millisecond))
	defer ctl.Close()

	p := lcPlayer()
	if err := ctl.Join(p); err != nil {
		t.Fatalf("join: %v", err)
	}
	ctl.Leave(p)

	time.Sleep(150 * time.Millisecond)
	select {
	case <-ctl.Done():
		t.Fatal("resident room ended on abandonment")
	default:
	}
	if froze {
		t.Fatal("resident room hibernated on abandonment")
	}
	// Drain still works: the freeze fn is wired for the explicit path.
	if !ctl.Hibernatable() {
		t.Fatal("resident room must stay drain-freezable")
	}
}

// gateHandler blocks the actor inside the FIRST OnJoin callback until released,
// so a test can deterministically queue commands behind a disposing one while
// the loop is parked.
type gateHandler struct {
	Base
	entered chan struct{} // closed once the actor is inside OnJoin
	release chan struct{} // close to let the actor proceed
	once    sync.Once
}

func (h *gateHandler) OnJoin(r Room, p Player) {
	h.once.Do(func() {
		close(h.entered)
		<-h.release
	})
}

func (h *gateHandler) CanHibernate() bool { return true }

// Regression: a cmdJoin queued in the buffered cmds channel behind a disposing
// command must return ErrRoomClosed once the loop exits — not block its caller
// forever. (Join used a bare `<-reply` instead of awaitReply, so a join racing
// the abandonment-grace hibernate wedged the session's update goroutine for the
// process lifetime.) The actor is gated inside OnJoin so channel FIFO ordering
// deterministically places cmdJoin behind the disposing cmdHibernate.
func TestJoinQueuedBehindDisposalReturnsRoomClosed(t *testing.T) {
	h := &gateHandler{entered: make(chan struct{}), release: make(chan struct{})}
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 4, MinPlayers: 1, Seed: 1, SeedSet: true}
	ctl := NewRoomRuntime("join-vs-dispose", h, cfg, Services{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer ctl.Close()
	rt := ctl.(*roomRuntime)

	// Join p1: the success reply is sent before OnJoin runs, then the actor
	// parks inside the gated callback.
	if err := ctl.Join(lcPlayer()); err != nil {
		t.Fatalf("join p1: %v", err)
	}
	<-h.entered

	// With the actor parked, enqueue the disposing Hibernate, THEN the Join —
	// waiting on the buffer depth between sends pins the FIFO order.
	hibErr := make(chan error, 1)
	go func() { hibErr <- ctl.Hibernate(func(Handler) error { return nil }) }()
	waitQueued(t, rt, 1)

	joinErr := make(chan error, 1)
	go func() {
		joinErr <- ctl.Join(Player{AccountID: "a2", Handle: "bob", Kind: KindMember, Conn: "c2"})
	}()
	waitQueued(t, rt, 2)

	close(h.release)

	select {
	case err := <-hibErr:
		if err != nil {
			t.Fatalf("hibernate: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hibernate did not return")
	}
	select {
	case err := <-joinErr:
		if !errors.Is(err, ErrRoomClosed) {
			t.Fatalf("queued join: got %v, want ErrRoomClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("join queued behind disposal hung — the deadlock this guards against")
	}
}

// waitQueued blocks until n commands sit in the room's cmds buffer.
func waitQueued(t *testing.T, rt *roomRuntime, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(rt.cmds) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d queued commands (have %d)", n, len(rt.cmds))
		}
		time.Sleep(time.Millisecond)
	}
}
