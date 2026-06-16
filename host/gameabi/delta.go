package gameabi

import (
	"hash/fnv"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/sdk"
)

// rosterFingerprint hashes the membership shape that determines baseline slot
// assignment: the count and each member's (AccountID, Kind) in roster order. A
// join, leave, or index shift changes it; Conn is intentionally EXCLUDED because
// it changes across hibernation (a resume must not be mistaken for a roster
// mutation — the epoch re-seed already handles resume). It is a backstop under
// the per-send epoch authority, not the primary resync.
func rosterFingerprint(roster []sdk.Player) uint64 {
	h := fnv.New64a()
	var lenb [2]byte
	lenb[0] = byte(len(roster))
	lenb[1] = byte(len(roster) >> 8)
	_, _ = h.Write(lenb[:])
	for _, p := range roster {
		_, _ = h.Write([]byte(p.AccountID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p.Kind))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// Host-side frame-delta ingestion (D4/D5/D7/D9). In ABI v2 the guest→host frame
// payload is the variable-length delta container (wire §4.5), not a bare packed
// grid. The host is the sole BASELINE AUTHORITY: per consumer slot it holds a
// previous packed grid, an epoch, and a present flag, and it returns the epoch
// the guest must stamp its baseline with. A non-keyframe delta applies iff its
// header epoch equals the slot epoch AND the slot has a baseline; otherwise the
// host drops it, bumps the slot epoch, and returns the new epoch (forcing the
// guest's next send to that slot to a keyframe). A keyframe is accepted
// regardless of epoch (self-contained), sets the baseline, and adopts the header
// epoch.
//
// The cache is host memory (one [FrameBytes]byte per consumer), actor-goroutine
// owned like h.cur/h.roster — no locking. It is NOT snapshotted (it is
// ephemeral host memory); on resume the epoch counter is re-seeded above any
// pre-snapshot high-water and every slot is marked not-present (D6/4.8).

// rosterCap is the fixed per-index baseline ceiling. The contract lives in
// kit/wire (wire.RosterCap): a shared protocol invariant the SDKs and the
// host all size against — changing it is ABI-affecting and lands in wire,
// every guest SDK, and this host in lockstep. Slots 0..rosterCap-1 are
// per-roster-index consumers; slot rosterCap is the broadcast (Identical)
// slot. The guest SDK drops sends for an index >= rosterCap, and the host's
// own bounds check (host.go) — not guest discipline — is what protects the
// slot table (D8). prev slots are lazily allocated so host memory tracks the
// ACTIVE roster, not the cap (~45 KiB per actively-sent-to consumer).
const rosterCap = wire.RosterCap

// broadcastSlot is the Identical baseline slot index within the cache.
const broadcastSlot = rosterCap

// numSlots is the cache size: rosterCap per-index slots + 1 broadcast slot.
const numSlots = rosterCap + 1

// baselineCache is the per-consumer baseline+epoch authority for one wasm room.
// epochSeq is the monotonic epoch the host stamps on a bump; it is re-seeded
// strictly above any pre-snapshot value on resume (D6).
type baselineCache struct {
	prev    [numSlots][]byte // wire.FrameBytes each, lazily allocated on first use
	epoch   [numSlots]uint32
	has     [numSlots]bool
	epochSeq uint32 // last issued epoch (highWater); next bump uses ++epochSeq
}

// buf returns slot's baseline buffer, allocating it on first use (lazy: host
// memory tracks the active roster, not the rosterCap ceiling).
func (c *baselineCache) buf(slot int) []byte {
	if c.prev[slot] == nil {
		c.prev[slot] = make([]byte, wire.FrameBytes)
	}
	return c.prev[slot]
}

// bump advances the epoch counter and assigns the new value to slot, marking it
// not-present so the next send to it is forced to a keyframe. Returns the new
// epoch the host hands back to the guest.
func (c *baselineCache) bump(slot int) uint32 {
	c.epochSeq++
	c.epoch[slot] = c.epochSeq
	c.has[slot] = false
	return c.epochSeq
}

// invalidateAll bumps the epoch counter once and marks every slot not-present,
// re-stamping each slot's epoch to the new value. Used on any roster mutation
// (join/leave/index shift) and on resume so the next send to each slot is
// epoch-rejected into a keyframe (D7/4.6/4.8). One bump for the whole sweep so
// the counter stays a tight high-water.
func (c *baselineCache) invalidateAll() {
	c.epochSeq++
	for i := 0; i < numSlots; i++ {
		c.epoch[i] = c.epochSeq
		c.has[i] = false
	}
}

// reseed sets the epoch counter strictly above highWater and marks every slot
// not-present (D6 hibernation resume). The baseline bytes are irrelevant once
// has[i] is false, so they are not cleared.
func (c *baselineCache) reseed(highWater uint32) {
	c.epochSeq = highWater
	for i := 0; i < numSlots; i++ {
		c.has[i] = false
	}
	// invalidateAll advances to highWater+1 and stamps every slot, giving the
	// "strictly greater than any pre-snapshot epoch" guarantee unconditionally.
	c.invalidateAll()
}

// applyResult reports the outcome of ingesting one delta container for a slot.
type applyResult struct {
	epoch   uint32 // the epoch to return to the guest
	applied bool   // true if the slot baseline advanced (a frame should be rendered)
}

// apply ingests a delta container b for the given slot, enforcing the epoch
// authority (D4) and the absent-baseline guard (D5). On a successful apply it
// advances prev[slot] in place and returns applied=true with the slot epoch; on
// a malformed/short container, an epoch mismatch, or a non-keyframe delta to a
// slot with no baseline, it bumps the slot epoch, drops the delta, and returns
// applied=false. It never panics and never reads out of bounds (CheckFrameDelta
// /ApplyFrameDelta enforce that). On a malformed container logFn is invoked once
// with the dropped-delta reason.
func (c *baselineCache) apply(slot int, b []byte, logFn func(reason string)) applyResult {
	if err := wire.CheckFrameDelta(b); err != nil {
		if logFn != nil {
			logFn(err.Error())
		}
		return applyResult{epoch: c.bump(slot), applied: false}
	}
	if wire.IsKeyframe(b) {
		// A keyframe is self-contained: accept regardless of epoch, overwrite the
		// whole baseline, adopt the header epoch.
		hdr := wire.DeltaEpoch(b)
		if err := wire.ApplyFrameDelta(c.buf(slot), b); err != nil {
			// Should not happen (CheckFrameDelta passed), but degrade-to-drop.
			if logFn != nil {
				logFn(err.Error())
			}
			return applyResult{epoch: c.bump(slot), applied: false}
		}
		c.epoch[slot] = hdr
		c.has[slot] = true
		return applyResult{epoch: hdr, applied: true}
	}
	// Non-keyframe delta: require a present baseline AND a matching epoch.
	// (has[slot] implies the slot buffer was allocated by a prior keyframe.)
	if !c.has[slot] || wire.DeltaEpoch(b) != c.epoch[slot] {
		return applyResult{epoch: c.bump(slot), applied: false}
	}
	if err := wire.ApplyFrameDelta(c.prev[slot], b); err != nil {
		if logFn != nil {
			logFn(err.Error())
		}
		return applyResult{epoch: c.bump(slot), applied: false}
	}
	return applyResult{epoch: c.epoch[slot], applied: true}
}

// reconcileBroadcast copies the broadcast slot's reconstructed grid into every
// ALLOCATED per-index slot and stamps it with the broadcast epoch (D7). Called
// after a successful Identical apply so a later per-player Send diffs against
// the baseline the broadcast left. Unallocated (never sent-to) slots are
// skipped instead of materialized — reconciling all rosterCap slots would copy
// ~45 MiB per broadcast at cap 1024. A skipped slot stays not-present, so the
// guest's next per-player Send to it is epoch-rejected into a keyframe (the
// standard recovery path); this mirrors the guest-side lazy reconcile exactly.
func (c *baselineCache) reconcileBroadcast(bcastEpoch uint32) {
	src := c.prev[broadcastSlot]
	for i := 0; i < rosterCap; i++ {
		if c.prev[i] == nil {
			c.has[i] = false
			continue
		}
		copy(c.prev[i], src)
		c.epoch[i] = bcastEpoch
		c.has[i] = true
	}
}
