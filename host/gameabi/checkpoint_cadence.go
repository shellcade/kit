package gameabi

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// CheckpointScheduler drives ONE room's periodic, non-destructive checkpoint
// cadence (room-hosting spec "Periodic Room Checkpoints", design D5): a jittered
// ticker (default ~30s ± jitter) that fires a checkpoint callback with a
// monotonic epoch starting at 0, suppressed while the room is lobby-idle.
//
// The scheduler is deliberately decoupled from room internals: it takes an Idle
// probe func and a Checkpoint callback rather than a Room/Handler, so it lives
// entirely in this package's durability seam and the caller wires it to whatever
// idle signal and capture path it owns (e.g. CheckpointHandler on the actor).
// The epoch is owned here (monotonic per room from 0); the caller passes it
// straight to CheckpointStore.Write.
type CheckpointScheduler struct {
	base   time.Duration
	jitter time.Duration
	clock  Clock
	idle   func() bool
	fire   func(ctx context.Context, epoch int64) error
	rng    *rand.Rand

	epoch    atomic.Int64 // next epoch to fire (atomic: the drain reads it via NextEpoch)
	lastIntv atomicDuration

	// fireMu guards the start of a fire against Close: a fire may begin only while
	// closed is false, and it registers on fireWG (under the lock) before it runs.
	// Close sets closed (under the lock) then Waits on fireWG, so it returns only
	// after any in-flight fire has completed and advanced the epoch — the drain's
	// close-then-NextEpoch sequence is then a true fence (the drain epoch is
	// strictly above every committed periodic write).
	fireMu sync.Mutex
	closed bool
	fireWG sync.WaitGroup

	closeOnce sync.Once
	done      chan struct{}
}

// Clock is the scheduler's view of time, injectable for tests. The production
// implementation (SystemClock) delegates to the stdlib; tests use a controllable
// fake that fires After channels on demand.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the real-time Clock backed by the stdlib.
type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// CadenceConfig configures one room's checkpoint scheduler. Base and Jitter are
// the per-game override hook: the caller picks them per game (default ~30s base
// when zero). Idle suppresses checkpoints for a lobby-idle room. Checkpoint
// captures+writes the snapshot for the given epoch (typically a closure over
// CheckpointHandler, the room's CheckpointStore, and its UUID).
type CadenceConfig struct {
	Base       time.Duration // default cadence; <=0 falls back to DefaultCheckpointInterval
	Jitter     time.Duration // +/- spread around Base; <0 treated as 0, clamped to <= Base
	Clock      Clock         // injectable; nil = SystemClock
	Idle       func() bool   // true => suppress this interval's checkpoint; nil = never idle
	Checkpoint func(ctx context.Context, epoch int64) error
	Rand       *rand.Rand // injectable jitter source; nil = a time-seeded source

	// StartEpoch is the FIRST epoch the scheduler fires (default 0). It exists for
	// the post-reclaim path: a room restored from a checkpoint at epoch N must seed
	// its new scheduler at N+1 so the next periodic write SUPERSEDES the restore
	// blob and advances the latest pointer — the pointer-epoch is strictly
	// monotonic across drain/reclaim cycles, forever (a fresh placement uses 0).
	StartEpoch int64
}

// DefaultCheckpointInterval is the spec's ~30s default cadence.
const DefaultCheckpointInterval = 30 * time.Second

// minInterval is the positive floor a jittered interval is clamped to (only
// reachable at the jitter==base extreme); it keeps clock.After from firing in a
// tight zero-delay loop.
const minInterval = 1 * time.Nanosecond

// NewCheckpointScheduler builds a scheduler from cfg. It does not start ticking
// until Run is called.
func NewCheckpointScheduler(cfg CadenceConfig) *CheckpointScheduler {
	base := cfg.Base
	if base <= 0 {
		base = DefaultCheckpointInterval
	}
	jitter := cfg.Jitter
	if jitter < 0 {
		jitter = 0
	}
	if jitter > base {
		jitter = base // never let an interval go negative
	}
	clock := cfg.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	idle := cfg.Idle
	if idle == nil {
		idle = func() bool { return false }
	}
	rng := cfg.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	s := &CheckpointScheduler{
		base:   base,
		jitter: jitter,
		clock:  clock,
		idle:   idle,
		fire:   cfg.Checkpoint,
		rng:    rng,
		done:   make(chan struct{}),
	}
	s.epoch.Store(cfg.StartEpoch)
	return s
}

// NextEpoch reports the epoch the scheduler would fire next — the value the drain
// reads (after Close, so no periodic tick can advance it concurrently) to pick a
// drain epoch strictly above every committed periodic checkpoint. Safe to call
// from another goroutine.
func (s *CheckpointScheduler) NextEpoch() int64 { return s.epoch.Load() }

// nextInterval returns base ± a uniform draw in [-jitter, +jitter].
func (s *CheckpointScheduler) nextInterval() time.Duration {
	if s.jitter == 0 {
		return s.base
	}
	// Uniform in [-jitter, +jitter].
	delta := time.Duration(s.rng.Int63n(int64(2*s.jitter)+1)) - s.jitter
	d := s.base + delta
	if d <= 0 {
		// Floor at 1ns. Only reachable at the jitter==base extreme with the
		// minimum draw (delta == -base => d == 0); a positive interval keeps
		// clock.After from firing immediately in a tight loop.
		d = minInterval
	}
	return d
}

// lastInterval reports the most recently armed interval (test introspection for
// jitter bounds).
func (s *CheckpointScheduler) lastInterval() time.Duration { return s.lastIntv.load() }

// Run drives the cadence until ctx is cancelled or Close is called. On each
// jittered tick it skips the checkpoint when Idle reports true (no epoch
// advance), otherwise it fires the Checkpoint callback with the next monotonic
// epoch (0, 1, 2, …) and advances only on a successful capture+write. A
// Checkpoint error is left to the callback to log; the epoch does not advance so
// the same epoch is retried next interval (a failed write must not burn an epoch
// the latest pointer never reached).
func (s *CheckpointScheduler) Run(ctx context.Context) {
	for {
		d := s.nextInterval()
		s.lastIntv.store(d)
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-s.clock.After(d):
		}
		if s.idle() {
			continue // lobby-idle: suppress this interval, do not advance the epoch
		}
		if !s.runFire(ctx) {
			return // Close fenced us before this fire could start
		}
	}
}

// runFire performs one checkpoint fire under the close fence: it registers on
// fireWG (so Close waits for it) only if the scheduler is not already closing,
// then fires and, on success, advances the epoch. It returns false when Close
// has fenced the cadence (the loop then exits without firing). A fire already
// past the gate runs to completion even as Close arrives — and Close blocks until
// it does (see the type doc).
func (s *CheckpointScheduler) runFire(ctx context.Context) bool {
	s.fireMu.Lock()
	if s.closed {
		s.fireMu.Unlock()
		return false
	}
	s.fireWG.Add(1)
	s.fireMu.Unlock()
	defer s.fireWG.Done()

	ep := s.epoch.Load()
	if err := s.fire(ctx, ep); err != nil {
		return true // retry the same epoch next interval (no advance)
	}
	s.epoch.Add(1)
	return true
}

// Close stops the scheduler and BLOCKS until any in-flight fire has completed (so
// the epoch it advanced is visible to a subsequent NextEpoch). Run returns
// promptly once no fire is running. Idempotent.
func (s *CheckpointScheduler) Close() {
	s.closeOnce.Do(func() {
		s.fireMu.Lock()
		s.closed = true
		s.fireMu.Unlock()
		close(s.done)
	})
	// Wait OUTSIDE closeOnce so a concurrent second Close also blocks until the
	// in-flight fire finishes (closeOnce's body runs only once, but every caller
	// must observe the fence).
	s.fireWG.Wait()
}

// atomicDuration is a tiny atomic wrapper so lastInterval can be read from a
// test goroutine while Run writes it.
type atomicDuration struct{ v atomic.Int64 }

func (a *atomicDuration) store(d time.Duration) { a.v.Store(int64(d)) }
func (a *atomicDuration) load() time.Duration   { return time.Duration(a.v.Load()) }
