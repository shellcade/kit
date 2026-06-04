//go:build wasip1 || tinygo.wasm

package gamekit

import (
	"context"
	"math/rand"
	"time"
)

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
