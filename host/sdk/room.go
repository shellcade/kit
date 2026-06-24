package sdk

import (
	"context"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultAbandonGrace is how long an emptied, hibernate-capable room waits for a
// rejoin before it auto-hibernates instead of ending (D9). A non-hibernatable
// room ignores this and ends immediately, as before.
const DefaultAbandonGrace = 60 * time.Second

// RoomOption tunes a room runtime at construction. Options are host-only (the
// lobby/matchmaker set them); games never see them.
type RoomOption func(*roomRuntime)

// WithAbandonHibernate wires the abandonment + drain hibernation path. fn is run
// ON the actor goroutine at a quiescent point with the live Handler (the same
// contract as RoomCtl.Hibernate's caller fn) and must freeze + persist it;
// grace is the empty-room reprieve before an abandoned room auto-hibernates
// (<=0 ⇒ DefaultAbandonGrace). Without this option a room is never hibernated:
// Hibernatable reports false and an abandoned room ends normally.
func WithAbandonHibernate(fn func(h Handler) error, grace time.Duration) RoomOption {
	if grace <= 0 {
		grace = DefaultAbandonGrace
	}
	return func(rt *roomRuntime) {
		rt.hibernateFn = fn
		rt.abandonGrace = grace
	}
}

// WithAbandonGrace overrides the empty-room reprieve window without wiring
// hibernation — the knob ephemeral rooms (and tests) use: a rejoin within the
// window finds the room alive; at expiry the lifecycle's abandonment action
// runs (<=0 ⇒ DefaultAbandonGrace).
func WithAbandonGrace(grace time.Duration) RoomOption {
	if grace <= 0 {
		grace = DefaultAbandonGrace
	}
	return func(rt *roomRuntime) { rt.abandonGrace = grace }
}

// WithResumed marks the runtime as resuming a RESTORED handler: the loop calls
// the handler's OnResume (if it implements Resumed) instead of OnStart, so the
// already-instantiated, memory-restored handler is not re-instantiated. Used by
// the lobby resume flow. A handler that does not implement Resumed falls back to
// OnStart.
func WithResumed() RoomOption {
	return func(rt *roomRuntime) { rt.resumed = true }
}

// NewRoomRuntime builds the engine runtime around a game's Handler and starts
// the single actor goroutine. It returns the lobby-facing RoomCtl.
func NewRoomRuntime(roomID string, h Handler, cfg RoomConfig, svc Services, opts ...RoomOption) RoomCtl {
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	seed := cfg.Seed
	if !cfg.SeedSet {
		hh := fnv.New64a()
		_, _ = hh.Write([]byte(roomID))
		seed = int64(hh.Sum64()) ^ start.UnixNano()
	}
	rt := &roomRuntime{
		roomID:       roomID,
		h:            h,
		cfg:          cfg,
		svc:          svc,
		start:        start,
		abandonGrace: DefaultAbandonGrace,
		rng:          rand.New(rand.NewSource(seed)),
		cmds:         make(chan command, 256),
		done:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		out:          map[Player]chan Frame{},
		timers:       map[TimerID]timerEntry{},
	}
	for _, o := range opts {
		o(rt)
	}
	var empty []Player
	rt.membersPub.Store(&empty)
	rt.phase.Store(&Phase{Name: "init", Open: false})
	go rt.loop()
	return rt
}

type cmdKind int

const (
	cmdJoin cmdKind = iota
	cmdLeave
	cmdInput
	cmdTimer
	cmdClose
	cmdHibernate
	cmdCheckpoint
)

type command struct {
	kind    cmdKind
	p       Player
	in      Input
	timerID TimerID
	reply   chan error
	freeze  func(h Handler) error // cmdHibernate: caller's snapshot fn, run on the actor
}

type timerEntry struct {
	fn   func(r Room)
	once bool
}

type roomRuntime struct {
	roomID string
	h      Handler
	cfg    RoomConfig
	svc    Services
	start  time.Time
	rng    *rand.Rand

	// hibernation wiring (host-only, set via WithAbandonHibernate): the snapshot
	// fn run on the actor for the abandonment + drain triggers, and the empty-room
	// grace before auto-hibernation. hibernateFn nil ⇒ the room is not
	// hibernatable (abandonment ends it normally, as before).
	hibernateFn  func(h Handler) error
	abandonGrace time.Duration
	resumed      bool // built via WithResumed: call OnResume, not OnStart, at loop entry

	cmds   chan command
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	// actor-owned state (touched only by the actor goroutine) ----------------
	members    []Player
	joined     []Player
	out        map[Player]chan Frame
	curEpoch   int64
	settled    bool
	ended      bool
	hibernated bool   // disposed via Hibernate (paused, not finished — no Result)
	endReason  string // why requestEnd fired, for the settle log
	pending    Result
	simTicker  *time.Ticker
	frameRate  *time.Ticker
	timers     map[TimerID]timerEntry
	nextTimer  TimerID

	// abandonment grace window: when an empty hibernate-capable room is given a
	// reprieve before it auto-ends, this is the live timer id (0 ⇒ none). A
	// rejoin cancels it; firing while still empty+unsettled hibernates the room.
	graceTimer TimerID

	// published for external (lobby) reads -----------------------------------
	membersPub  atomic.Pointer[[]Player]
	outPub      sync.Map // Player -> chan Frame
	phase       atomic.Pointer[Phase]
	inputCtx    atomic.Int32 // current InputContext; zero value is CtxNav
	resultPub   atomic.Pointer[Result]
	staleLogged atomic.Bool
}

// ---- actor goroutine ------------------------------------------------------

func (rt *roomRuntime) loop() {
	if rt.resumed {
		if rh, ok := rt.h.(Resumed); ok {
			rt.call(rh.OnResume)
		} else {
			rt.call(rt.h.OnStart) // handler can't resume: fall back to a fresh start
		}
	} else {
		rt.call(rt.h.OnStart)
	}
	rt.maybeSettle()
	for !rt.settled && !rt.hibernated {
		select {
		case <-rt.ctx.Done():
			// Defensive: rt.ctx is rooted at Background today, so this branch (and its
			// "context-canceled" end reason) cannot fire until a parent is wired.
			rt.requestEnd(Result{Mode: rt.cfg.Mode}, "context-canceled")
		case c := <-rt.cmds:
			rt.handle(c)
		case now := <-tickerC(rt.simTicker):
			rt.call(func(r Room) { rt.h.OnTick(r, now) })
		case <-tickerC(rt.frameRate):
			rt.pushFrame()
		}
		rt.maybeSettle()
	}
}

// hibernatable reports whether this room's Handler opts into hibernation AND the
// host wired a snapshot fn. Read-only on the Handler reference (immutable), so
// it is safe both on and off the actor goroutine.
func (rt *roomRuntime) hibernatable() bool {
	if rt.hibernateFn == nil {
		return false
	}
	hc, ok := rt.h.(HibernationCapable)
	return ok && hc.CanHibernate()
}

func tickerC(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func (rt *roomRuntime) handle(c command) {
	switch c.kind {
	case cmdJoin:
		if rt.settled || rt.ended {
			c.reply <- errRoomClosed
			return
		}
		if rt.cfg.Capacity > 0 && len(rt.members) >= rt.cfg.Capacity {
			c.reply <- errRoomFull
			return
		}
		rt.cancelGrace() // a rejoin cancels any pending abandonment hibernation
		ch := make(chan Frame, 1)
		rt.members = append(rt.members, c.p)
		rt.joined = append(rt.joined, c.p)
		rt.out[c.p] = ch
		rt.outPub.Store(c.p, ch)
		rt.publishMembers()
		c.reply <- nil
		rt.logInfo("room: player joined",
			slog.String("handle", c.p.Handle), slog.String("kind", string(c.p.Kind)),
			slog.Int("members", len(rt.members)))
		rt.call(func(r Room) { rt.h.OnJoin(r, c.p) })
	case cmdLeave:
		if !rt.isMember(c.p) {
			return
		}
		rt.removeMember(c.p)
		rt.logInfo("room: player left",
			slog.String("handle", c.p.Handle), slog.Int("members", len(rt.members)))
		rt.call(func(r Room) { rt.h.OnLeave(r, c.p) })
		if len(rt.members) == 0 && len(rt.joined) > 0 {
			// Abandonment, by lifecycle: a resident room ignores it entirely
			// (the world keeps ticking); an ephemeral room gets the same
			// grace reprieve but ENDS at its expiry (no snapshot, no resume
			// entry); a resumable hibernate-capable room gets the grace then
			// auto-hibernates; otherwise it ends now, as it always has.
			switch {
			case rt.cfg.Lifecycle == LifecycleResident:
				// keep running
			case rt.cfg.Lifecycle == LifecycleEphemeral:
				rt.logInfo("room: abandoned — ending after grace", slog.Duration("grace", rt.abandonGrace))
				rt.armGrace(func() { rt.requestEnd(Result{Mode: rt.cfg.Mode}, "abandoned") })
			case rt.hibernatable():
				rt.logInfo("room: abandoned — hibernating after grace", slog.Duration("grace", rt.abandonGrace))
				rt.armGrace(func() { rt.doHibernate(rt.hibernateFn) })
			default:
				rt.requestEnd(Result{Mode: rt.cfg.Mode}, "abandoned")
			}
		}
	case cmdInput:
		if rt.settled || rt.ended || !rt.isMember(c.p) {
			return
		}
		rt.call(func(r Room) { rt.h.OnInput(r, c.p, c.in) })
	case cmdTimer:
		e, ok := rt.timers[c.timerID]
		if !ok {
			return
		}
		if e.once {
			delete(rt.timers, c.timerID)
		}
		rt.call(e.fn)
	case cmdClose:
		rt.requestEnd(Result{Mode: rt.cfg.Mode}, "closed")
	case cmdHibernate:
		rt.handleHibernate(c)
	case cmdCheckpoint:
		rt.handleCheckpoint(c)
	}
}

// armGrace schedules an abandonment-hibernation attempt after abandonGrace. It
// reuses the timer machinery (a once-timer firing on the actor) so cancellation
// and ctx teardown are already correct. The fire checks the room is still
// empty + unsettled before acting (a rejoin between arm and fire cancels it, but
// this is the belt-and-braces check). Caller is on the actor goroutine.
// armGrace schedules the abandonment action (hibernate or end, by lifecycle)
// after the grace window; a rejoin within the window cancels it.
func (rt *roomRuntime) armGrace(action func()) {
	rt.nextTimer++
	id := rt.nextTimer
	rt.graceTimer = id
	rt.timers[id] = timerEntry{once: true, fn: func(Room) {
		if rt.graceTimer != id {
			return // superseded/cancelled
		}
		rt.graceTimer = 0
		if rt.settled || rt.ended || rt.hibernated || len(rt.members) != 0 {
			return
		}
		action() // empty + unsettled: the lifecycle's abandonment action
	}}
	rt.scheduleTimer(id, rt.abandonGrace)
}

// cancelGrace stops a pending abandonment grace timer (a rejoin or disposal).
// Caller is on the actor goroutine.
func (rt *roomRuntime) cancelGrace() {
	if rt.graceTimer != 0 {
		delete(rt.timers, rt.graceTimer)
		rt.graceTimer = 0
	}
}

// handleHibernate runs an explicit RoomCtl.Hibernate request on the actor at a
// quiescent point (the command loop guarantees no Handler callback is on the
// stack here). It replies on c.reply exactly once.
//
// Reply-before-dispose ordering matters: a successful hibernate disposes the room
// (disposeHibernated cancels rt.ctx). awaitReply unblocks on EITHER the reply or
// rt.ctx.Done(); if disposal's cancel became visible before the success reply was
// sent, awaitReply's ctx-done branch could observe an empty reply chan and report
// a FALSE ErrRoomClosed for a hibernation that actually succeeded (drain then
// logs "room is closed" / "froze 0 rooms"). So we send the success reply FIRST,
// then dispose — the reply send happens-before the cancel, so awaitReply's
// primary reply case is guaranteed to have a value waiting. (A failed freeze does
// NOT dispose, so its reply ordering is immaterial; settled/ended/hibernated is
// the genuine closed case.)
func (rt *roomRuntime) handleHibernate(c command) {
	if rt.settled || rt.ended || rt.hibernated {
		c.reply <- errRoomClosed
		return
	}
	if c.freeze == nil {
		c.reply <- errRoomClosed
		return
	}
	rt.cancelGrace()
	if err := c.freeze(rt.h); err != nil {
		c.reply <- err // freeze failed: room stays live, ordering immaterial
		return
	}
	c.reply <- nil         // success reply BEFORE disposal cancels the ctx
	rt.disposeHibernated() // cancels rt.ctx; reply already delivered
}

// handleCheckpoint runs a NON-destructive checkpoint request on the actor at a
// quiescent point (no Handler callback on the stack), handing fn the live
// Handler so the caller can snapshot it (e.g. gameabi.CheckpointHandler +
// CheckpointStore.Write). Unlike Hibernate, the room is NOT disposed: it keeps
// running afterward (room-hosting spec "Periodic Room Checkpoints" / drain
// snapshots, design D5). A settled/ended/hibernated room replies ErrRoomClosed
// without calling fn; an fn error is returned to the caller and the room stays
// live. It replies on c.reply exactly once.
func (rt *roomRuntime) handleCheckpoint(c command) {
	if rt.settled || rt.ended || rt.hibernated {
		c.reply <- errRoomClosed
		return
	}
	if c.freeze == nil {
		c.reply <- errRoomClosed
		return
	}
	c.reply <- c.freeze(rt.h)
}

// doHibernate freezes the room via fn (run with the live Handler, no callback on
// the stack) then disposes the room WITHOUT a normal end: no Result, no
// leaderboard post, no DNF backfill — player streams just close. If fn fails the
// room is NOT disposed by this path (the caller decides; for the grace/drain
// path the room then ends normally on the next abandonment check or stays put).
// Caller is on the actor goroutine.
func (rt *roomRuntime) doHibernate(fn func(h Handler) error) error {
	if fn == nil {
		return errRoomClosed
	}
	rt.cancelGrace()
	if err := fn(rt.h); err != nil {
		return err
	}
	rt.logInfo("room: hibernated", slog.Duration("age", time.Since(rt.start)))
	rt.disposeHibernated()
	return nil
}

// disposeHibernated tears the room down as paused (not finished): close every
// player stream so sessions see the room go away, stop tickers, cancel the ctx,
// and signal Done — but publish NO result and run NO OnClose settle path. The
// loop exits because hibernated is set. Caller is on the actor goroutine.
func (rt *roomRuntime) disposeHibernated() {
	if rt.hibernated || rt.settled {
		return
	}
	rt.hibernated = true
	rt.phase.Store(&Phase{Name: "hibernated", Open: false})
	for p, ch := range rt.out {
		close(ch)
		rt.outPub.Delete(p)
	}
	rt.out = map[Player]chan Frame{}
	rt.stopTickers()
	rt.cancel()
	close(rt.done)
}

// call invokes a Handler callback with a fresh, epoch-stamped Room handle that
// becomes stale the moment the callback returns.
// call runs one handler callback on the actor goroutine, wrapped in a recover so
// a panic in host code (a game's host function, a frame send to a vanished
// player, a bug in the engine's per-callback plumbing) faults ONLY this room
// instead of unwinding the actor goroutine and crashing the whole process —
// which would take every other live room on the peer down with it. Every
// callback funnels through here (OnStart/Join/Leave/Input/Tick/Frame/Close and
// timers), so this is the one place isolation has to hold. A recovered room is
// ended; the loop's maybeSettle then tears it down on the normal path (and a
// re-panic inside the resulting OnClose is caught here too, so teardown still
// completes).
func (rt *roomRuntime) call(fn func(r Room)) {
	rt.curEpoch++
	defer func() {
		rt.curEpoch++
		if v := recover(); v != nil {
			if rt.svc.Log != nil {
				rt.svc.Log.Error("room actor panic — settling room",
					slog.String("room", rt.roomID),
					slog.Any("panic", v),
					slog.String("stack", string(debug.Stack())),
				)
			}
			rt.requestEnd(Result{Mode: rt.cfg.Mode}, "panic")
		}
	}()
	h := &roomHandle{rt: rt, epoch: rt.curEpoch}
	fn(h)
}

// logInfo emits an engine lifecycle event on the room's logger (nil in some
// tests). svc.Log already carries room+slug attrs (services.Factory.For).
func (rt *roomRuntime) logInfo(msg string, args ...any) {
	if rt.svc.Log != nil {
		rt.svc.Log.Info(msg, args...)
	}
}

func (rt *roomRuntime) requestEnd(res Result, reason string) {
	if rt.ended || rt.settled {
		return
	}
	rt.ended = true
	rt.pending = res
	rt.endReason = reason
}

func (rt *roomRuntime) maybeSettle() {
	if rt.ended && !rt.settled {
		rt.settle()
	}
}

func (rt *roomRuntime) settle() {
	res := rt.finalize(rt.pending)
	rt.settled = true
	rt.logInfo("room: settled",
		slog.String("reason", rt.endReason),
		slog.Duration("age", time.Since(rt.start)),
		slog.Int("joined", len(rt.joined)))
	rt.resultPub.Store(&res)
	rt.phase.Store(&Phase{Name: "settled", Open: false, Settled: true, Result: &res})
	// close every player's stream exactly once
	for p, ch := range rt.out {
		close(ch)
		rt.outPub.Delete(p)
	}
	rt.out = map[Player]chan Frame{}
	rt.call(func(r Room) { rt.h.OnClose(r) })
	rt.stopTickers()
	rt.cancel()
	close(rt.done)
}

// finalize backfills a dnf PlayerResult for every joined player the game omitted
// (the roster-of-record guarantee).
func (rt *roomRuntime) finalize(res Result) Result {
	res.Mode = rt.cfg.Mode
	have := map[Player]bool{}
	for _, pr := range res.Rankings {
		have[pr.Player] = true
	}
	for _, p := range rt.joined {
		if !have[p] {
			res.Rankings = append(res.Rankings, PlayerResult{Player: p, Status: StatusDNF})
		}
	}
	return res
}

func (rt *roomRuntime) pushFrame() {
	snap := frozen{members: rt.copyMembers(), cfg: rt.cfg, now: time.Now()}
	rt.call(func(r Room) { rt.h.OnFrame(r, snap) })
}

// ---- membership helpers (actor-only) --------------------------------------

func (rt *roomRuntime) isMember(p Player) bool {
	for _, m := range rt.members {
		if m == p {
			return true
		}
	}
	return false
}

func (rt *roomRuntime) removeMember(p Player) {
	next := rt.members[:0]
	for _, m := range rt.members {
		if m != p {
			next = append(next, m)
		}
	}
	rt.members = next
	if ch, ok := rt.out[p]; ok {
		close(ch)
		delete(rt.out, p)
		rt.outPub.Delete(p)
	}
	rt.publishMembers()
}

func (rt *roomRuntime) copyMembers() []Player {
	cp := make([]Player, len(rt.members))
	copy(cp, rt.members)
	return cp
}

func (rt *roomRuntime) publishMembers() {
	cp := rt.copyMembers()
	rt.membersPub.Store(&cp)
}

func (rt *roomRuntime) send(p Player, f Frame) {
	ch, ok := rt.out[p]
	if !ok {
		return
	}
	coalesceSend(ch, f)
}

// coalesceSend performs a non-blocking, depth-1, drop/coalesce-newest send: if
// the buffer already holds an undelivered frame, the stale one is discarded and
// the newest kept. A slow consumer never blocks the caller.
func coalesceSend(ch chan Frame, f Frame) {
	select {
	case ch <- f:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- f:
		default:
		}
	}
}

func (rt *roomRuntime) stopTickers() {
	if rt.simTicker != nil {
		rt.simTicker.Stop()
		rt.simTicker = nil
	}
	if rt.frameRate != nil {
		rt.frameRate.Stop()
		rt.frameRate = nil
	}
}

func (rt *roomRuntime) enqueue(c command) {
	select {
	case rt.cmds <- c:
	case <-rt.ctx.Done():
	}
}

// ---- RoomCtl (lobby-facing, called off the actor goroutine) ---------------

func (rt *roomRuntime) Join(p Player) error {
	reply := make(chan error, 1)
	select {
	case rt.cmds <- command{kind: cmdJoin, p: p, reply: reply}:
	case <-rt.ctx.Done():
		return errRoomClosed
	}
	// awaitReply, not a bare <-reply: a cmdJoin can land in the buffered cmds
	// channel an instant before the loop exits on settle/hibernate (e.g. the
	// abandonment-grace timer fires first), and the loop never drains queued
	// commands — a bare receive would wedge the calling session goroutine
	// forever. The join success reply is sent before any disposal cancels the
	// ctx, so no false ErrRoomClosed is possible.
	return rt.awaitReply(reply)
}

func (rt *roomRuntime) Leave(p Player)           { rt.enqueue(command{kind: cmdLeave, p: p}) }
func (rt *roomRuntime) Input(p Player, in Input) { rt.enqueue(command{kind: cmdInput, p: p, in: in}) }

func (rt *roomRuntime) Members() []Player {
	if pp := rt.membersPub.Load(); pp != nil {
		return *pp
	}
	return nil
}

func (rt *roomRuntime) Frames(p Player) <-chan Frame {
	if ch, ok := rt.outPub.Load(p); ok {
		return ch.(chan Frame)
	}
	return nil
}

func (rt *roomRuntime) Done() <-chan struct{} { return rt.done }

func (rt *roomRuntime) InputContext() InputContext { return InputContext(rt.inputCtx.Load()) }

func (rt *roomRuntime) Snapshot() Phase {
	ph := rt.phase.Load()
	if ph == nil {
		return Phase{}
	}
	out := *ph
	if !out.Settled && !out.Deadline.IsZero() {
		out.Remaining = time.Until(out.Deadline)
		if out.Remaining < 0 {
			out.Remaining = 0
		}
	}
	return out
}

func (rt *roomRuntime) Result() (Result, bool) {
	if rp := rt.resultPub.Load(); rp != nil {
		return *rp, true
	}
	return Result{}, false
}

func (rt *roomRuntime) Close() error {
	rt.enqueue(command{kind: cmdClose})
	return nil
}

func (rt *roomRuntime) Hibernatable() bool { return rt.hibernatable() }

// Hibernate enqueues a quiesce request and blocks until fn has run on the actor
// (with the live Handler, no callback on the stack) and the room is disposed, or
// the room is already gone. fn is the caller's snapshot step (e.g.
// gameabi.SnapshotHandler + a store Put); after it returns nil the room is
// disposed as paused, not finished (no Result, no leaderboard post). A
// settled/already-hibernated room returns ErrRoomClosed without calling fn.
func (rt *roomRuntime) Hibernate(fn func(h Handler) error) error {
	reply := make(chan error, 1)
	select {
	case rt.cmds <- command{kind: cmdHibernate, freeze: fn, reply: reply}:
	case <-rt.ctx.Done():
		return errRoomClosed
	}
	return rt.awaitReply(reply)
}

// Checkpoint enqueues a NON-destructive quiesce request and blocks until fn has
// run on the actor (with the live Handler, no callback on the stack) — the room
// keeps running afterward. fn is the caller's snapshot step (e.g.
// gameabi.CheckpointHandler + CheckpointStore.Write at the room's next epoch).
// A settled/ended/hibernated room returns ErrRoomClosed without calling fn; an
// fn error is returned and the room stays live. This is the durability seam for
// periodic checkpoints and drain snapshots (room-hosting spec, design D5).
func (rt *roomRuntime) Checkpoint(fn func(h Handler) error) error {
	reply := make(chan error, 1)
	select {
	case rt.cmds <- command{kind: cmdCheckpoint, freeze: fn, reply: reply}:
	case <-rt.ctx.Done():
		return errRoomClosed
	}
	return rt.awaitReply(reply)
}

// awaitReply blocks for a command's reply, but unblocks with ErrRoomClosed if
// the room disposes first. A command can land in the buffered cmds channel an
// instant before settle/hibernate cancels the ctx and exits the loop, so the
// loop never processes it and would never reply — awaitReply turns that into the
// room-closed contract instead of a deadlock. The buffered reply is preferred so
// a value the loop already produced is still observed even as ctx closes.
func (rt *roomRuntime) awaitReply(reply chan error) error {
	select {
	case err := <-reply:
		return err
	case <-rt.ctx.Done():
		// Prefer a reply that raced in just before ctx closed.
		select {
		case err := <-reply:
			return err
		default:
			return errRoomClosed
		}
	}
}

// frozen is the read-only Snapshot handed to OnFrame.
type frozen struct {
	members []Player
	cfg     RoomConfig
	now     time.Time
}

func (f frozen) Members() []Player  { return f.members }
func (f frozen) Config() RoomConfig { return f.cfg }
func (f frozen) Now() time.Time     { return f.now }

// ---- the Room handle (game-facing, valid only inside a callback) ----------

type roomHandle struct {
	rt    *roomRuntime
	epoch int64
}

func (h *roomHandle) valid() bool {
	if h.epoch != h.rt.curEpoch {
		if h.rt.svc.Log != nil && !h.rt.staleLogged.Swap(true) {
			h.rt.svc.Log.Warn("stale room handle used outside its callback", slog.String("room", h.rt.roomID))
		}
		return false
	}
	return true
}

func (h *roomHandle) Members() []Player {
	if !h.valid() {
		return nil
	}
	return h.rt.copyMembers()
}

func (h *roomHandle) Has(p Player) bool {
	if !h.valid() {
		return false
	}
	return h.rt.isMember(p)
}

func (h *roomHandle) Count() int {
	if !h.valid() {
		return 0
	}
	return len(h.rt.members)
}

func (h *roomHandle) Config() RoomConfig { return h.rt.cfg }
func (h *roomHandle) Rand() *rand.Rand   { return h.rt.rng }
func (h *roomHandle) Now() time.Time     { return time.Now() }

func (h *roomHandle) Send(p Player, f Frame) {
	if !h.valid() {
		return
	}
	h.rt.send(p, f)
}

func (h *roomHandle) Identical(f Frame) {
	if !h.valid() {
		return
	}
	for _, p := range h.rt.members {
		h.rt.send(p, f)
	}
}

func (h *roomHandle) BroadcastFunc(compose func(p Player) Frame) {
	if !h.valid() {
		return
	}
	for _, p := range h.rt.members {
		h.rt.send(p, compose(p))
	}
}

func (h *roomHandle) After(d time.Duration, fn func(r Room)) TimerID {
	if !h.valid() {
		return 0
	}
	rt := h.rt
	rt.nextTimer++
	id := rt.nextTimer
	rt.timers[id] = timerEntry{fn: fn, once: true}
	rt.scheduleTimer(id, d)
	return id
}

// scheduleTimer fires once-timer id onto the actor after d (or never, if the
// room's ctx is cancelled first). The timer entry must already be registered.
// Used by After and by the abandonment grace window.
func (rt *roomRuntime) scheduleTimer(id TimerID, d time.Duration) {
	go func() {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			rt.enqueue(command{kind: cmdTimer, timerID: id})
		case <-rt.ctx.Done():
		}
	}()
}

func (h *roomHandle) Every(d time.Duration, fn func(r Room)) TimerID {
	if !h.valid() || d <= 0 {
		return 0
	}
	rt := h.rt
	rt.nextTimer++
	id := rt.nextTimer
	rt.timers[id] = timerEntry{fn: fn, once: false}
	go func() {
		tk := time.NewTicker(d)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				rt.enqueue(command{kind: cmdTimer, timerID: id})
			case <-rt.ctx.Done():
				return
			}
		}
	}()
	return id
}

func (h *roomHandle) Cancel(id TimerID) {
	if !h.valid() {
		return
	}
	delete(h.rt.timers, id)
}

func (h *roomHandle) SetSimRate(d time.Duration) {
	if !h.valid() {
		return
	}
	if h.rt.simTicker != nil {
		h.rt.simTicker.Stop()
		h.rt.simTicker = nil
	}
	if d > 0 {
		h.rt.simTicker = time.NewTicker(d)
	}
}

func (h *roomHandle) SetFrameRate(d time.Duration) {
	if !h.valid() {
		return
	}
	if h.rt.frameRate != nil {
		h.rt.frameRate.Stop()
		h.rt.frameRate = nil
	}
	if d > 0 {
		h.rt.frameRate = time.NewTicker(d)
	}
}

func (h *roomHandle) SetPhase(name string, open bool, deadline time.Time) {
	if !h.valid() {
		return
	}
	h.rt.phase.Store(&Phase{Name: name, Open: open, Deadline: deadline})
}

func (h *roomHandle) SetInputContext(ctx InputContext) {
	if !h.valid() {
		return
	}
	h.rt.inputCtx.Store(int32(ctx))
}

func (h *roomHandle) End(res Result) {
	if !h.valid() {
		return
	}
	h.rt.requestEnd(res, "game")
}

func (h *roomHandle) Result() (Result, bool) { return h.rt.Result() }
func (h *roomHandle) Services() Services     { return h.rt.svc }
func (h *roomHandle) Log() *slog.Logger      { return h.rt.svc.Log }
