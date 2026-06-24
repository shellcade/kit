package sdk

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/canvas"
)

func frameWith(r rune) Frame {
	f := canvas.New()
	f.SetRune(0, 0, r, canvas.Style{})
	return f
}

// coalescing: depth-1, drop/coalesce-newest.
func TestCoalesceSendKeepsNewest(t *testing.T) {
	ch := make(chan Frame, 1)
	coalesceSend(ch, frameWith('a'))
	coalesceSend(ch, frameWith('b'))
	coalesceSend(ch, frameWith('c'))
	if len(ch) != 1 {
		t.Fatalf("buffer holds %d frames, want 1", len(ch))
	}
	got := <-ch
	if got.Cells[0][0].Rune != 'c' {
		t.Fatalf("got %q, want newest 'c'", got.Cells[0][0].Rune)
	}
}

// a game that ends on the rune 'q', ranking current members as finished.
type endGame struct{ GameBase }

func (endGame) Meta() GameMeta                       { return GameMeta{Slug: "end", MaxPlayers: 5} }
func (endGame) NewRoom(RoomConfig, Services) Handler { return &endHandler{} }

type endHandler struct{ Base }

func (endHandler) OnInput(r Room, p Player, in Input) {
	r.Send(p, frameWith(in.Rune))
	if in.Rune == 'q' {
		var rk []PlayerResult
		for i, m := range r.Members() {
			rk = append(rk, PlayerResult{Player: m, Rank: i + 1, Metric: 100, Status: StatusFinished})
		}
		r.End(Result{Rankings: rk})
	}
}

func mkPlayer(id string) Player { return Player{AccountID: id, Handle: id, Kind: KindMember} }

// a game that panics inside a callback on the rune 'p' — standing in for a
// host-side fault (a bad host fn, a frame send to a vanished player).
type panicGame struct{ GameBase }

func (panicGame) Meta() GameMeta                       { return GameMeta{Slug: "panic", MaxPlayers: 5} }
func (panicGame) NewRoom(RoomConfig, Services) Handler { return &panicHandler{} }

type panicHandler struct{ Base }

func (panicHandler) OnInput(r Room, p Player, in Input) {
	if in.Rune == 'p' {
		panic("boom inside a callback")
	}
}

// A panic on the actor goroutine must fault ONLY its room (settle it), never
// unwind the goroutine and crash the process — which would take every other
// live room on the peer down. The room ends; a brand-new room still works
// afterward, proving the process survived.
func TestActorPanicSettlesRoomNotProcess(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
	ctl := NewRoomRuntime("rp", panicGame{}.NewRoom(cfg, Services{}), cfg, Services{})
	if err := ctl.Join(mkPlayer("a")); err != nil {
		t.Fatalf("join: %v", err)
	}
	ctl.Input(mkPlayer("a"), RuneInput('p')) // panics inside OnInput
	select {
	case <-ctl.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("panicking room did not settle — recover did not fault it")
	}

	// The process is still alive (we got here), and a fresh room still runs.
	cfg2 := RoomConfig{Mode: ModeQuick, Capacity: 1}
	ok := NewRoomRuntime("rp2", endGame{}.NewRoom(cfg2, Services{}), cfg2, Services{})
	if err := ok.Join(mkPlayer("b")); err != nil {
		t.Fatalf("a new room must still work after another room panicked: %v", err)
	}
	ok.Input(mkPlayer("b"), RuneInput('q'))
	<-ok.Done()
}

// roster-of-record: a joined-then-left player still appears as dnf, and the
// game ranking the rest is enough — the engine backfills.
func TestRosterOfRecordBackfillsDNF(t *testing.T) {
	ctl := NewRoomRuntime("r1", endGame{}.NewRoom(RoomConfig{Mode: ModeQuick, Capacity: 5}, Services{}), RoomConfig{Mode: ModeQuick, Capacity: 5}, Services{})
	a, b, c := mkPlayer("a"), mkPlayer("b"), mkPlayer("c")
	for _, p := range []Player{a, b, c} {
		if err := ctl.Join(p); err != nil {
			t.Fatalf("join %s: %v", p.AccountID, err)
		}
	}
	ctl.Leave(c)                 // enqueued before the input below (FIFO)
	ctl.Input(a, RuneInput('q')) // game ends ranking the live members (a, b)
	<-ctl.Done()

	res, ok := ctl.Result()
	if !ok {
		t.Fatal("no result after Done")
	}
	if len(res.Rankings) != 3 {
		t.Fatalf("rankings=%d, want 3 (every joined player)", len(res.Rankings))
	}
	var cStatus Status
	finished := 0
	for _, pr := range res.Rankings {
		if pr.Player == c {
			cStatus = pr.Status
		}
		if pr.Status == StatusFinished {
			finished++
		}
	}
	if cStatus != StatusDNF {
		t.Fatalf("left player c status=%q, want dnf", cStatus)
	}
	if finished != 2 {
		t.Fatalf("finished=%d, want 2 (a,b)", finished)
	}
}

// End fires exactly once; Done is closed; further input is a no-op.
func TestSettleOnce(t *testing.T) {
	cfg := RoomConfig{Mode: ModeSolo, Capacity: 1}
	ctl := NewRoomRuntime("r2", endGame{}.NewRoom(cfg, Services{}), cfg, Services{})
	a := mkPlayer("a")
	if err := ctl.Join(a); err != nil {
		t.Fatal(err)
	}
	ctl.Input(a, RuneInput('q'))
	<-ctl.Done()
	// further input must not panic or reopen the room
	ctl.Input(a, RuneInput('x'))
	if _, ok := ctl.Result(); !ok {
		t.Fatal("result missing")
	}
	// Done channel is closed (second receive returns immediately)
	select {
	case <-ctl.Done():
	default:
		t.Fatal("Done not closed")
	}
}

// capacity is enforced atomically.
func TestCapacityEnforced(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 2}
	ctl := NewRoomRuntime("r3", endGame{}.NewRoom(cfg, Services{}), cfg, Services{})
	if err := ctl.Join(mkPlayer("a")); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Join(mkPlayer("b")); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Join(mkPlayer("c")); err == nil {
		t.Fatal("third join should fail (capacity 2)")
	}
	_ = ctl.Close()
	<-ctl.Done()
}

type timerEdgeHandler struct {
	Base
	everyZeroID TimerID
	everyNegID  TimerID
	afterZeroID TimerID
	calls       chan string
}

func (h *timerEdgeHandler) OnStart(r Room) {
	h.everyZeroID = r.Every(0, func(Room) { h.calls <- "every-zero" })
	h.everyNegID = r.Every(-time.Second, func(Room) { h.calls <- "every-neg" })
	h.afterZeroID = r.After(0, func(Room) { h.calls <- "after-zero" })
}

func TestEveryNonPositiveIsNoop(t *testing.T) {
	h := &timerEdgeHandler{calls: make(chan string, 4)}
	ctl := NewRoomRuntime("timer-edge", h, RoomConfig{Mode: ModeSolo, Capacity: 1}, Services{})
	defer ctl.Close()

	select {
	case got := <-h.calls:
		if got != "after-zero" {
			t.Fatalf("first timer callback=%q, want after-zero", got)
		}
	case <-time.After(time.Second):
		t.Fatal("After(0) did not fire")
	}
	if h.everyZeroID != 0 {
		t.Fatalf("Every(0) id=%d, want 0", h.everyZeroID)
	}
	if h.everyNegID != 0 {
		t.Fatalf("Every(-d) id=%d, want 0", h.everyNegID)
	}
	if h.afterZeroID == 0 {
		t.Fatal("After(0) returned 0")
	}
	select {
	case got := <-h.calls:
		t.Fatalf("unexpected non-positive Every callback %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTestRoomEveryNonPositiveIsNoop(t *testing.T) {
	h := &timerEdgeHandler{calls: make(chan string, 4)}
	r := NewTestRoomFor(h, RoomConfig{Mode: ModeSolo, Capacity: 1}, Services{})
	r.Start()
	if h.everyZeroID != 0 {
		t.Fatalf("Every(0) id=%d, want 0", h.everyZeroID)
	}
	if h.everyNegID != 0 {
		t.Fatalf("Every(-d) id=%d, want 0", h.everyNegID)
	}
	r.Advance(0)
	select {
	case got := <-h.calls:
		if got != "after-zero" {
			t.Fatalf("first timer callback=%q, want after-zero", got)
		}
	default:
		t.Fatal("After(0) did not fire in TestRoom")
	}
	select {
	case got := <-h.calls:
		t.Fatalf("unexpected non-positive Every callback %q", got)
	default:
	}
}

func recvFrame(t *testing.T, ch <-chan Frame) Frame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(time.Second):
		t.Fatal("no frame received within 1s")
		return Frame{}
	}
}

// Two connections of the SAME account (same AccountID/Handle, different Conn) are
// distinct memberships with their own frame streams. Regression for the same-SSH
// -key-twice freeze.
func TestSameAccountDistinctConnections(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 5}
	ctl := NewRoomRuntime("rc", endGame{}.NewRoom(cfg, Services{}), cfg, Services{})
	p1 := Player{AccountID: "a", Handle: "h", Kind: KindMember, Conn: "1"}
	p2 := Player{AccountID: "a", Handle: "h", Kind: KindMember, Conn: "2"}
	if err := ctl.Join(p1); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Join(p2); err != nil {
		t.Fatal(err)
	}
	if len(ctl.Members()) != 2 {
		t.Fatalf("members=%d, want 2 distinct seats", len(ctl.Members()))
	}
	ch1, ch2 := ctl.Frames(p1), ctl.Frames(p2)
	if ch1 == nil || ch2 == nil {
		t.Fatal("missing per-connection frame stream")
	}
	// each connection receives ITS OWN frame (the endGame echoes the typed rune).
	ctl.Input(p1, RuneInput('x'))
	ctl.Input(p2, RuneInput('y'))
	if got := recvFrame(t, ch1).Cells[0][0].Rune; got != 'x' {
		t.Fatalf("conn1 frame rune=%q, want 'x' (its channel was orphaned?)", got)
	}
	if got := recvFrame(t, ch2).Cells[0][0].Rune; got != 'y' {
		t.Fatalf("conn2 frame rune=%q, want 'y'", got)
	}
	_ = ctl.Close()
	<-ctl.Done()
}

// concurrency smoke: drain frames while inputs arrive; -race must stay clean.
func TestConcurrentDrainRace(t *testing.T) {
	cfg := RoomConfig{Mode: ModeQuick, Capacity: 4}
	ctl := NewRoomRuntime("r4", endGame{}.NewRoom(cfg, Services{}), cfg, Services{})
	a := mkPlayer("a")
	if err := ctl.Join(a); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ch := ctl.Frames(a)
		for range ch { // drain until closed at settle
		}
	}()
	for i := 0; i < 200; i++ {
		ctl.Input(a, RuneInput('x'))
	}
	ctl.Input(a, RuneInput('q'))
	<-ctl.Done()
	wg.Wait()
}

// syncLogBuf is a goroutine-safe sink for asserting engine log lines (the
// actor goroutine writes them).
type syncLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncLogBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLogBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lifecycle INFO logs: join/leave/settle with an end reason, asserted through
// the real actor goroutine.
func TestLifecycleLogs(t *testing.T) {
	buf := &syncLogBuf{}
	svc := Services{Log: slog.New(slog.NewTextHandler(buf, nil))}
	cfg := RoomConfig{Mode: ModeQuick}
	ctl := NewRoomRuntime("room-log-test", endGame{}.NewRoom(cfg, svc), cfg, svc)
	p := Player{Handle: "alice", Kind: KindMember, Conn: "c1"}
	if err := ctl.Join(p); err != nil {
		t.Fatalf("join: %v", err)
	}
	ctl.Leave(p)
	<-ctl.Done() // empty non-resident, non-hibernatable room ends immediately
	got := buf.String()
	for _, want := range []string{
		`msg="room: player joined"`, `handle=alice`,
		`msg="room: player left"`,
		`msg="room: settled"`, `reason=abandoned`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output missing %q\n--- got:\n%s", want, got)
		}
	}
}
