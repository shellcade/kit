//go:build wasip1 || tinygo.wasm

package gamekit

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

// ---- implementation -----------------------------------------------------------

// room is the live Room implementation, refreshed per callback from the
// decoded CallContext.
type room struct {
	ctx callContext
	rng *rand.Rand
}

func (r *room) Members() []Player  { return r.ctx.members }
func (r *room) Count() int         { return len(r.ctx.members) }
func (r *room) Config() RoomConfig { return r.ctx.cfg }
func (r *room) Rand() *rand.Rand   { return r.rng }
func (r *room) Now() time.Time     { return time.Unix(0, r.ctx.nowUnixNanos) }
func (r *room) Settled() bool      { return r.ctx.settled }

func (r *room) Has(p Player) bool {
	for _, m := range r.ctx.members {
		if m == p {
			return true
		}
	}
	return false
}

func (r *room) index(p Player) int {
	for i, m := range r.ctx.members {
		if m == p {
			return i
		}
	}
	return -1
}

func (r *room) Send(p Player, f *Frame) {
	idx := r.index(p)
	if idx < 0 || f == nil {
		return
	}
	m := alloc(encodeFrame(f))
	hostSend(uint64(idx), m.Offset())
	m.Free()
}

func (r *room) Identical(f *Frame) {
	if f == nil {
		return
	}
	m := alloc(encodeFrame(f))
	hostIdentical(m.Offset())
	m.Free()
}

func (r *room) SetInputContext(ctx InputContext) { hostSetInputContext(uint64(ctx)) }

func (r *room) End(res Result) {
	m := alloc(encodeResult(res, r.ctx.members))
	hostEnd(m.Offset())
	m.Free()
}

func (r *room) Post(res Result) {
	m := alloc(encodeResult(res, r.ctx.members))
	hostPost(m.Offset())
	m.Free()
}

func (r *room) Log(msg string) {
	m := allocStr(msg)
	hostLog(1, m.Offset())
	m.Free()
}

func (r *room) Services() Services {
	return Services{Accounts: accountStore{r}, Config: configStore{}}
}

type accountStore struct{ r *room }

func (s accountStore) For(p Player) Account {
	idx := s.r.index(p)
	if idx < 0 {
		// Departed player delivered by the host as the final roster entry
		// (the leave callback): resolve by account id.
		for i, m := range s.r.ctx.members {
			if m.AccountID == p.AccountID {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return nil
	}
	return account{idx: idx, p: s.r.ctx.members[idx]}
}

type account struct {
	idx int
	p   Player
}

func (a account) ID() string     { return a.p.AccountID }
func (a account) Handle() string { return a.p.Handle }
func (a account) Kind() Kind     { return a.p.Kind }
func (a account) Store() KVStore { return kvStore{idx: a.idx} }

type kvStore struct{ idx int }

func (k kvStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	km := allocStr(key)
	off := hostKVGet(uint64(k.idx), km.Offset())
	km.Free()
	v, ok := readBytesFree(off)
	return v, ok, nil
}

func (k kvStore) Set(_ context.Context, key string, value []byte, rule MergeRule) error {
	km, vm, rm := allocStr(key), alloc(value), allocStr(string(rule))
	hostKVSet(uint64(k.idx), km.Offset(), vm.Offset(), rm.Offset())
	km.Free()
	vm.Free()
	rm.Free()
	return nil
}

func (k kvStore) Delete(_ context.Context, key string) error {
	km := allocStr(key)
	hostKVDelete(uint64(k.idx), km.Offset())
	km.Free()
	return nil
}

type configStore struct{}

func (configStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	km := allocStr(key)
	off := hostConfigGet(km.Offset())
	km.Free()
	v, ok := readBytesFree(off)
	return v, ok, nil
}
