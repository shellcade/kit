package sdk

import (
	"log/slog"
	"math/rand"
	"sort"
	"time"
)

// TestRoom is a published Room test double. It drives a Handler's callbacks
// synchronously with an injectable clock and records emitted frames, published
// phases, and the settled Result — so a game's logic can be unit-tested with no
// goroutine, channel, or socket.
type TestRoom struct {
	cfg RoomConfig
	h   Handler
	svc Services
	rng *rand.Rand

	Clock   time.Time
	members []Player
	joined  []Player

	Frames   map[Player][]Frame // every frame pushed, per player
	Phases   []Phase            // every SetPhase call
	InputCtx InputContext       // latest SetInputContext value (zero = CtxNav)
	Ended    bool
	Res      Result

	timers map[TimerID]testTimer
	nextID TimerID
}

type testTimer struct {
	fireAt time.Time
	period time.Duration
	fn     func(r Room)
	every  bool
}

// NewTestRoom builds a TestRoom for game g with the given config. The clock
// starts at an arbitrary fixed instant; use Advance to move it.
func NewTestRoom(g Game, cfg RoomConfig, svc Services) *TestRoom {
	return NewTestRoomFor(g.NewRoom(cfg, svc), cfg, svc)
}

// NewTestRoomFor builds a TestRoom driving an explicit Handler. Useful for
// white-box tests that need direct access to the handler's state.
func NewTestRoomFor(h Handler, cfg RoomConfig, svc Services) *TestRoom {
	if svc.Log == nil {
		svc.Log = slog.Default()
	}
	seed := cfg.Seed
	if !cfg.SeedSet {
		seed = 1
	}
	return &TestRoom{
		cfg:    cfg,
		h:      h,
		svc:    svc,
		rng:    rand.New(rand.NewSource(seed)),
		Clock:  time.Unix(1_700_000_000, 0),
		Frames: map[Player][]Frame{},
		timers: map[TimerID]testTimer{},
	}
}

// Start invokes OnStart.
func (t *TestRoom) Start() { t.h.OnStart(t) }

// Join admits a player and invokes OnJoin.
func (t *TestRoom) Join(p Player) {
	if t.Ended {
		return
	}
	t.members = append(t.members, p)
	t.joined = append(t.joined, p)
	t.h.OnJoin(t, p)
}

// Leave removes a player and invokes OnLeave.
func (t *TestRoom) Leave(p Player) {
	out := t.members[:0]
	for _, m := range t.members {
		if m != p {
			out = append(out, m)
		}
	}
	t.members = out
	t.h.OnLeave(t, p)
}

// Input delivers an input and invokes OnInput.
func (t *TestRoom) Input(p Player, in Input) {
	if t.Ended {
		return
	}
	t.h.OnInput(t, p, in)
}

// Tick invokes OnTick at the current clock.
func (t *TestRoom) Tick() { t.h.OnTick(t, t.Clock) }

// Frame invokes OnFrame with a frozen snapshot at the current clock.
func (t *TestRoom) Frame() {
	snap := frozen{members: append([]Player(nil), t.members...), cfg: t.cfg, now: t.Clock}
	t.h.OnFrame(t, snap)
}

// Advance moves the clock forward and fires any due timers (in time order).
func (t *TestRoom) Advance(d time.Duration) {
	target := t.Clock.Add(d)
	for {
		var nextID TimerID
		var next testTimer
		found := false
		for id, tm := range t.timers {
			if !tm.fireAt.After(target) && (!found || tm.fireAt.Before(next.fireAt)) {
				nextID, next, found = id, tm, true
			}
		}
		if !found {
			break
		}
		t.Clock = next.fireAt
		if next.every {
			next.fireAt = next.fireAt.Add(next.period)
			t.timers[nextID] = next
		} else {
			delete(t.timers, nextID)
		}
		next.fn(t)
		if t.Ended {
			break
		}
	}
	t.Clock = target
}

// LastFrame returns the most recent frame pushed to p, if any.
func (t *TestRoom) LastFrame(p Player) (Frame, bool) {
	fs := t.Frames[p]
	if len(fs) == 0 {
		return Frame{}, false
	}
	return fs[len(fs)-1], true
}

// ---- Room implementation --------------------------------------------------

func (t *TestRoom) Members() []Player { return append([]Player(nil), t.members...) }
func (t *TestRoom) Has(p Player) bool {
	for _, m := range t.members {
		if m == p {
			return true
		}
	}
	return false
}
func (t *TestRoom) Count() int         { return len(t.members) }
func (t *TestRoom) Config() RoomConfig { return t.cfg }
func (t *TestRoom) Rand() *rand.Rand   { return t.rng }
func (t *TestRoom) Now() time.Time     { return t.Clock }

func (t *TestRoom) Send(p Player, f Frame) { t.Frames[p] = append(t.Frames[p], f) }
func (t *TestRoom) Identical(f Frame) {
	for _, p := range t.members {
		t.Frames[p] = append(t.Frames[p], f)
	}
}
func (t *TestRoom) BroadcastFunc(compose func(p Player) Frame) {
	for _, p := range t.members {
		t.Frames[p] = append(t.Frames[p], compose(p))
	}
}

func (t *TestRoom) After(d time.Duration, fn func(r Room)) TimerID {
	t.nextID++
	t.timers[t.nextID] = testTimer{fireAt: t.Clock.Add(d), fn: fn}
	return t.nextID
}
func (t *TestRoom) Every(d time.Duration, fn func(r Room)) TimerID {
	t.nextID++
	t.timers[t.nextID] = testTimer{fireAt: t.Clock.Add(d), period: d, fn: fn, every: true}
	return t.nextID
}
func (t *TestRoom) Cancel(id TimerID)          { delete(t.timers, id) }
func (t *TestRoom) SetSimRate(time.Duration)   {}
func (t *TestRoom) SetFrameRate(time.Duration) {}

func (t *TestRoom) SetPhase(name string, open bool, deadline time.Time) {
	ph := Phase{Name: name, Open: open, Deadline: deadline}
	if !deadline.IsZero() {
		ph.Remaining = deadline.Sub(t.Clock)
	}
	t.Phases = append(t.Phases, ph)
}

// SetInputContext records the latest published input context, exposed via
// InputCtx for assertions.
func (t *TestRoom) SetInputContext(ctx InputContext) { t.InputCtx = ctx }

func (t *TestRoom) End(res Result) {
	if t.Ended {
		return
	}
	t.Ended = true
	// backfill dnf vs roster-of-record, mirroring the engine
	res.Mode = t.cfg.Mode
	have := map[Player]bool{}
	for _, pr := range res.Rankings {
		have[pr.Player] = true
	}
	for _, p := range t.joined {
		if !have[p] {
			res.Rankings = append(res.Rankings, PlayerResult{Player: p, Status: StatusDNF})
		}
	}
	sort.SliceStable(res.Rankings, func(i, j int) bool { return res.Rankings[i].Rank < res.Rankings[j].Rank })
	t.Res = res
	t.h.OnClose(t)
}

func (t *TestRoom) Result() (Result, bool) {
	if t.Ended {
		return t.Res, true
	}
	return Result{}, false
}
func (t *TestRoom) Services() Services { return t.svc }
func (t *TestRoom) Log() *slog.Logger  { return t.svc.Log }

// LastPhase returns the most recently published phase.
func (t *TestRoom) LastPhase() (Phase, bool) {
	if len(t.Phases) == 0 {
		return Phase{}, false
	}
	return t.Phases[len(t.Phases)-1], true
}
