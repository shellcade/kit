package game

import (
	"context"
	"math/rand"
	"time"
)

// Room is the authoring surface: local reads answered from the cached
// CallContext (zero host calls), effects via host functions. A Room handle is
// valid only inside the callback that received it.
type Room interface {
	// Local reads.
	Members() []Player
	Has(p Player) bool
	Count() int
	Config() RoomConfig
	Rand() *rand.Rand
	Now() time.Time
	Settled() bool

	// Effects (host calls).
	Send(p Player, f *Frame)
	Identical(f *Frame)
	SetInputContext(ctx InputContext)
	End(res Result)
	Post(res Result)
	Log(msg string)

	Services() Services
}

// Handler is the game's per-room behavior — the lean wasm surface (OnWake is
// the host heartbeat; there are no ticks, timers, or frame callbacks).
type Handler interface {
	OnStart(r Room)
	OnJoin(r Room, p Player)
	OnLeave(r Room, p Player)
	OnInput(r Room, p Player, in Input)
	OnWake(r Room)
	OnClose(r Room)
}

// Base supplies no-op defaults so a game overrides only what it needs.
type Base struct{}

func (Base) OnStart(Room)                {}
func (Base) OnJoin(Room, Player)         {}
func (Base) OnLeave(Room, Player)        {}
func (Base) OnInput(Room, Player, Input) {}
func (Base) OnWake(Room)                 {}
func (Base) OnClose(Room)                {}

// Game is the module entry: static metadata plus the room behavior factory.
type Game interface {
	Meta() GameMeta
	NewRoom(cfg RoomConfig, svc Services) Handler
}

// ---- Services ----------------------------------------------------------------

// KVStore is the durable per-user KV, already namespaced to this game and one
// account (the HOST derives both — a guest can never name another namespace).
// Signatures mirror the native sdk so KV patterns port unchanged; the context
// is accepted and ignored (the host bounds the call).
type KVStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, rule MergeRule) error
	Delete(ctx context.Context, key string) error
}

// Account is a live, account-scoped handle for a Player.
type Account interface {
	ID() string
	Handle() string
	Kind() Kind
	Store() KVStore
}

// AccountStore yields an Account for a Player.
type AccountStore interface{ For(p Player) Account }

// ConfigStore is the slug-bound, read-only per-game config surface.
type ConfigStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
}

// Services is the ABI v1 service bundle (no chat, no spectate).
type Services struct {
	Accounts AccountStore
	Config   ConfigStore
}
