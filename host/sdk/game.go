package sdk

import (
	"log/slog"
	"math/rand"
	"time"
)

// Game is the registry entry. NewRoom returns the game's behavior (a Handler);
// the engine builds the Room runtime around it. sealed() keeps the interface
// growable without breaking out-of-tree games.
type Game interface {
	Meta() GameMeta
	NewRoom(cfg RoomConfig, svc Services) Handler
	sealed()
}

// Handler is the game's per-room behavior. The engine invokes these callbacks
// one at a time on a single actor goroutine, passing a Room handle valid only
// for the duration of the call. Embed Base for no-op defaults + the seal.
type Handler interface {
	OnStart(r Room)
	OnJoin(r Room, p Player)
	OnLeave(r Room, p Player)
	OnInput(r Room, p Player, in Input)
	OnTick(r Room, now time.Time)
	OnFrame(r Room, snap Snapshot)
	OnClose(r Room)
	handlerSeal()
}

// TimerID identifies a scheduled timer.
type TimerID uint64

// Room is the engine-provided handle the game drives. It is valid only inside a
// callback; a stale handle used afterward (or off-thread) is a logged no-op.
type Room interface {
	// roster / config / clock
	Members() []Player
	Has(p Player) bool
	Count() int
	Config() RoomConfig
	Rand() *rand.Rand
	Now() time.Time

	// push frames (engine hides coalescing, single-writer, close)
	Send(p Player, f Frame)
	Identical(f Frame)
	BroadcastFunc(compose func(p Player) Frame)

	// engine-owned timing
	After(d time.Duration, fn func(r Room)) TimerID
	Every(d time.Duration, fn func(r Room)) TimerID
	Cancel(id TimerID)
	SetSimRate(d time.Duration)
	SetFrameRate(d time.Duration)

	// phase publication for the lobby
	SetPhase(name string, open bool, deadline time.Time)

	// input-context publication for the lobby's play loop: the game declares
	// which InputContext applies to its current phase so Back (q/Esc) resolves
	// consistently for every game. Defaults to CtxNav until first set.
	SetInputContext(ctx InputContext)

	// settle exactly once
	End(res Result)
	Result() (Result, bool)

	Services() Services
	Log() *slog.Logger
}

// RoomCtl is the engine control surface held by the lobby/hub. The game never
// sees it; the lobby never sees Room.
type RoomCtl interface {
	Join(p Player) error
	Leave(p Player)
	Input(p Player, in Input)
	Members() []Player
	Frames(p Player) <-chan Frame
	Done() <-chan struct{}
	Snapshot() Phase
	// InputContext is the game's currently published input context, so the lobby
	// play loop resolves Back (q/Esc) appropriately. Defaults to CtxNav.
	InputContext() InputContext
	Result() (Result, bool)
	Close() error

	// Hibernatable reports whether the room's Handler can be frozen and resumed
	// (the lobby/drain path only hibernates rooms that say yes). It is answered
	// off the actor goroutine from the immutable Handler reference, so it is
	// safe to call any time.
	Hibernatable() bool

	// Hibernate quiesces the room and runs fn ON the actor goroutine at a point
	// with no Handler callback on the stack, handing fn the live Handler so the
	// caller can freeze it (e.g. gameabi.SnapshotHandler). After fn returns the
	// room is disposed WITHOUT delivering a normal end: player frame streams
	// close (players see the room go away, not a settled result), no Result is
	// published, no leaderboard post or DNF backfill runs — the room is paused,
	// not finished. fn runs exactly once; a settled/already-hibernated room
	// returns errRoomClosed without calling it. Hibernate blocks until fn has
	// run (or the room is gone).
	Hibernate(fn func(h Handler) error) error

	// Checkpoint runs fn ON the actor goroutine at a quiescent point (no Handler
	// callback on the stack), handing fn the live Handler so the caller can take a
	// NON-destructive snapshot (e.g. gameabi.CheckpointHandler + CheckpointStore)
	// — the room keeps running afterward. This is the durability seam for periodic
	// checkpoints and drain snapshots (room-hosting spec "Periodic Room
	// Checkpoints", design D5), distinct from the disposing Hibernate. A
	// settled/ended/hibernated room returns errRoomClosed without calling fn; an
	// fn error is returned and the room stays live. Checkpoint blocks until fn has
	// run (or the room is gone).
	Checkpoint(fn func(h Handler) error) error
}

// HibernationCapable is the capability a Handler advertises to opt into
// hibernation. The engine asserts it against the room's Handler to answer
// RoomCtl.Hibernatable; a Handler that does not implement it is never frozen
// (its room ends normally on abandonment / drain instead). gameabi's wasm
// handler implements it; in-process Go games do not (their state is not
// portable across a process restart), so they are unaffected.
type HibernationCapable interface {
	// CanHibernate reports whether this handler is, right now, in a state that
	// can be frozen (e.g. a live, un-faulted wasm instance). A handler that
	// implements the interface but returns false is treated as not hibernatable.
	CanHibernate() bool
}

// Resumed is the capability a RESTORED Handler advertises so the engine resumes
// it WITHOUT re-running OnStart (which would re-instantiate and clobber the
// restored state). A room built with WithResumed calls OnResume in place of
// OnStart exactly once, at loop entry; the handler uses it to re-establish
// engine-owned timing (sim/frame rate) it would normally set in OnStart, with
// no fresh instantiation. A handler that does not implement it falls back to
// OnStart (harmless for a never-resumed handler).
type Resumed interface {
	OnResume(r Room)
}

// GameBase is embedded by a Game implementation to satisfy the unexported
// sealed() method, keeping Game growable.
type GameBase struct{}

func (GameBase) sealed() {}

// Base is embedded by a Handler implementation. It satisfies the unexported
// handlerSeal() and supplies a no-op default for every callback, so a minimal
// game overrides only what it needs.
type Base struct{}

func (Base) handlerSeal()                {}
func (Base) OnStart(Room)                {}
func (Base) OnJoin(Room, Player)         {}
func (Base) OnLeave(Room, Player)        {}
func (Base) OnInput(Room, Player, Input) {}
func (Base) OnTick(Room, time.Time)      {}
func (Base) OnFrame(Room, Snapshot)      {}
func (Base) OnClose(Room)                {}
