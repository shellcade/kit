//go:build wasip1 || tinygo.wasm

package game

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

// Send ships a per-player frame as a v2 delta container (D4/D9). It packs the
// frame once (encodeFrame enforces canonical-zero), builds a delta against the
// slot's baseline — or a keyframe when the slot is not present (first send,
// roster change) or the delta meets/exceeds the budget — sends it, and mirrors
// the host-returned epoch. If the host rejected the delta (returned a different
// epoch: hibernation restore, baseline loss), the SAME frame is immediately
// re-sent as a keyframe — we are still on-stack, keyframes are unconditionally
// accepted, and without the retry this render would be silently lost to the
// viewer (and a restored room would fail byte-identical hibernation
// conformance). One retry only: a keyframe cannot be rejected.
func (r *room) Send(p Player, f *Frame) {
	idx := r.index(p)
	if idx < 0 || f == nil || idx >= rosterCap {
		return
	}
	packed := encodeFrame(f)
	sentEpoch := baselineEpoch[idx]
	wasDelta := baselinePresent[idx]
	payload := buildSendPayload(idx, packed)
	m := alloc(payload)
	returned := uint32(hostSend(uint64(idx), m.Offset()))
	m.Free()
	if returned != sentEpoch && wasDelta {
		// Rejected delta: resync to the host's epoch and retry as a keyframe.
		baselinePresent[idx] = false
		baselineEpoch[idx] = returned
		retry := buildSendPayload(idx, packed) // keyframe form (slot not present)
		m = alloc(retry)
		returned = uint32(hostSend(uint64(idx), m.Offset()))
		m.Free()
	}
	commitBaseline(idx, packed, returned)
}

// Identical broadcasts one frame to every player. It diffs against the broadcast
// baseline; on accept it reconciles EVERY per-index baseline (copy the frame in,
// stamp the returned epoch) so a later per-player Send diffs against the correct
// baseline (D7). A keyframe is sent when the broadcast slot is not present, and
// a rejected delta is immediately retried as a keyframe (same rationale as Send:
// no silently lost render, byte-identical hibernation conformance).
func (r *room) Identical(f *Frame) {
	if f == nil {
		return
	}
	packed := encodeFrame(f)
	sentEpoch := baselineEpoch[broadcastSlot]
	wasDelta := baselinePresent[broadcastSlot]
	payload := buildSendPayload(broadcastSlot, packed)
	m := alloc(payload)
	returned := uint32(hostIdentical(m.Offset()))
	m.Free()
	if returned != sentEpoch && wasDelta {
		// Rejected delta: resync to the host's epoch and retry as a keyframe.
		baselinePresent[broadcastSlot] = false
		baselineEpoch[broadcastSlot] = returned
		retry := buildSendPayload(broadcastSlot, packed) // keyframe form
		m = alloc(retry)
		returned = uint32(hostIdentical(m.Offset()))
		m.Free()
	}
	// Reconcile the broadcast slot and every ALLOCATED per-index baseline.
	// Unallocated (never sent-to) slots are NOT allocated here — committing
	// to all rosterCap slots would materialize the whole lazy table on the
	// first broadcast. An unallocated slot is instead left not-present, so a
	// later per-player Send to it opens with a keyframe (unconditionally
	// accepted) — same recovery path as a roster change.
	commitBaseline(broadcastSlot, packed, returned)
	for i := 0; i < rosterCap; i++ {
		if baselines[i] != nil {
			commitBaseline(i, packed, returned)
		} else {
			baselinePresent[i] = false
		}
	}
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
