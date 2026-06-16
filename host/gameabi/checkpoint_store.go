package gameabi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shellcade/kit/v2/host/blobstore"
)

// CheckpointStore is the NON-DESTRUCTIVE, versioned room-durability path (design
// D5, room-hosting spec "Periodic Room Checkpoints") — distinct from the
// disposing HibernationStore in this package. Where HibernationStore writes a
// single flat snapshots/<roomID> object and deletes it on restore, the
// checkpoint store writes monotonic, MAC'd snapshots/<roomID>/<epoch> objects
// behind a monotonically-advanced latest pointer, so a room can be checkpointed
// repeatedly while it keeps running and a slow lower-epoch write can never
// regress the pointer off a later (higher-epoch) drain write.
//
// The payload is opaque to the store: it is the same deterministic snapshot blob
// the hibernation codec already produces (SnapshotHandler) — the checkpoint path
// does not invent a new format, it only adds versioned keying + a server-side
// MAC over whatever bytes it is handed.
//
// PRECONDITION — single writer per room: Write MUST be serialized per roomID
// (each room is driven by its own actor/scheduler, which is the only thing that
// checkpoints that room). The store takes no lock and makes no assertion of this
// — coordination deliberately lives in the actor, not here. The monotonic
// latest-pointer advance and the never-overwrite-an-epoch check are correct only
// under this precondition; concurrent writers to one room would race both. Across
// DIFFERENT rooms Write is safe to call concurrently (the backing Store is).
type CheckpointStore struct {
	store  blobstore.Store
	sealer blobstore.Sealer
}

// NewCheckpointStore wraps a blobstore.Store with a Sealer that MACs every blob
// with a server-side key held outside the wasm sandbox. nil store/sealer is a
// programmer error guarded per-method (matching HibernationStore), never a
// panic.
func NewCheckpointStore(store blobstore.Store, sealer blobstore.Sealer) *CheckpointStore {
	return &CheckpointStore{store: store, sealer: sealer}
}

// ErrNoCheckpoint reports that a room has no checkpoint yet (the latest pointer
// is absent). Distinct from ErrCheckpointCorrupt: missing is "nothing to
// restore", corrupt is "quarantine this room".
var ErrNoCheckpoint = errors.New("gameabi: no checkpoint for room")

// ErrCheckpointCorrupt reports a checkpoint that failed integrity verification
// (the MAC did not verify, or the blob was truncated/tampered in storage). It
// wraps blobstore.ErrSealVerify. The re-hydration path maps this to PER-ROOM
// quarantine (room-hosting spec "Re-Hydration") — never to peer death. By
// contract NO payload byte escapes ReadLatest when this is returned, so the
// restore path can rely on verify-before-write.
var ErrCheckpointCorrupt = errors.New("gameabi: checkpoint failed integrity verification")

func (s *CheckpointStore) configured() error {
	if s == nil || s.store == nil || s.sealer == nil {
		return fmt.Errorf("gameabi: checkpoint store not configured")
	}
	return nil
}

// Write seals payload, PUTs it at the never-overwritten epoch key
// snapshots/<roomID>/<epoch>, then advances the latest pointer to that epoch —
// but only if epoch is higher than the epoch the pointer currently names. That
// advance is a read-check-then-PUT: readPointerEpoch reads the current pointer,
// Write compares, and PUTs the new pointer only when this epoch is strictly
// higher. The single PUT of the pointer object is atomic (S3/Tigris PUT of one
// object is atomic; the in-memory double matches), so a concurrent READER sees
// the old pointer or the new one, never a torn value — but that PUT atomicity
// buys only torn-read safety, NOT the monotonic advance. The monotonic guarantee
// rests on the single-writer-per-room precondition (see the type doc): with no
// concurrent writer for this room, the read-compare-PUT sequence cannot race, so
// a slow lower-epoch Write that completes AFTER a higher-epoch Write reads the
// higher pointer, fails the compare, and leaves the pointer untouched. This is
// the "late periodic PUT cannot clobber the drain snapshot" guarantee: the slow
// write still lands its own (distinct) epoch object, but never regresses the
// pointer off the higher drain epoch.
//
// An epoch key that already exists is never overwritten in place — but it is
// not an error either: under the single-writer-per-room precondition the only
// way the object can exist is OUR OWN earlier attempt at this epoch that was
// interrupted between the epoch PUT and the pointer advance (e.g. a per-fire
// timeout). The blob PUT is atomic, so the stored object is a complete, sealed
// snapshot from that attempt; Write resumes by skipping the payload PUT and
// finishing the pointer advance, so a retry at the same epoch CONVERGES instead
// of failing "already written" forever (which would brick the room's periodic
// cadence AND its drain, both of which retry the unadvanced epoch). The probe
// is a read-then-PUT; like the pointer advance it is race-free only under the
// single-writer precondition (it is not an independent guard against a
// concurrent writer).
func (s *CheckpointStore) Write(ctx context.Context, roomID string, epoch int64, payload []byte) error {
	if err := s.configured(); err != nil {
		return err
	}
	if roomID == "" {
		return fmt.Errorf("gameabi: checkpoint: empty room id")
	}
	key := blobstore.CheckpointKey(roomID, epoch)
	_, exists, err := s.store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("gameabi: checkpoint: probe epoch key: %w", err)
	}
	if !exists {
		if err := s.store.Put(ctx, key, s.sealer.Seal(payload)); err != nil {
			return fmt.Errorf("gameabi: checkpoint: put epoch object: %w", err)
		}
	}
	// exists: resume an interrupted earlier attempt at this epoch — keep its
	// (complete, sealed) object and just finish the pointer advance below.
	// Advance the latest pointer only if this epoch is newer than what it names,
	// so an out-of-order (slow, lower-epoch) write cannot regress the pointer.
	// Read-check-then-PUT; race-free under the single-writer-per-room precondition.
	cur, ok, err := s.readPointerEpoch(ctx, roomID)
	if err != nil {
		return err
	}
	if ok && cur >= epoch {
		return nil // a newer (or equal) checkpoint already owns the pointer
	}
	if err := s.store.Put(ctx, blobstore.LatestPointerKey(roomID), []byte(key)); err != nil {
		return fmt.Errorf("gameabi: checkpoint: swap latest pointer: %w", err)
	}
	return nil
}

// ReadLatest resolves the latest pointer, GETs the checkpoint it names, and
// VERIFIES the MAC before returning any payload byte (verify-before-write: the
// restore path writes these bytes into guest memory). A verification failure
// returns ErrCheckpointCorrupt (wrapping blobstore.ErrSealVerify) and a nil
// payload — no partial payload escapes. An absent pointer returns
// ErrNoCheckpoint.
func (s *CheckpointStore) ReadLatest(ctx context.Context, roomID string) (payload []byte, epoch int64, err error) {
	if err := s.configured(); err != nil {
		return nil, 0, err
	}
	if roomID == "" {
		return nil, 0, fmt.Errorf("gameabi: checkpoint: empty room id")
	}
	ptr, ok, err := s.store.Get(ctx, blobstore.LatestPointerKey(roomID))
	if err != nil {
		return nil, 0, fmt.Errorf("gameabi: checkpoint: read latest pointer: %w", err)
	}
	if !ok {
		return nil, 0, ErrNoCheckpoint
	}
	key := string(ptr)
	ep, err := epochFromKey(roomID, key)
	if err != nil {
		// A pointer that does not name a well-formed epoch key is corruption of
		// the durability state, not a missing checkpoint.
		return nil, 0, fmt.Errorf("%w: malformed latest pointer %q: %v", ErrCheckpointCorrupt, key, err)
	}
	sealed, ok, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, 0, fmt.Errorf("gameabi: checkpoint: get %q: %w", key, err)
	}
	if !ok {
		// The pointer names an object that is gone — treat as corrupt durability
		// state so the room is quarantined rather than silently losing the body.
		return nil, 0, fmt.Errorf("%w: latest pointer names absent object %q", ErrCheckpointCorrupt, key)
	}
	open, err := s.sealer.Open(sealed)
	if err != nil {
		// Verify BEFORE returning: no payload byte escapes a failed MAC.
		return nil, 0, fmt.Errorf("%w: %w", ErrCheckpointCorrupt, err)
	}
	return open, ep, nil
}

// readPointerEpoch returns the epoch the latest pointer currently names, or
// ok=false when there is no pointer yet. A malformed pointer is a hard error
// (durability-state corruption) rather than a silent reset.
func (s *CheckpointStore) readPointerEpoch(ctx context.Context, roomID string) (int64, bool, error) {
	ptr, ok, err := s.store.Get(ctx, blobstore.LatestPointerKey(roomID))
	if err != nil {
		return 0, false, fmt.Errorf("gameabi: checkpoint: read latest pointer: %w", err)
	}
	if !ok {
		return 0, false, nil
	}
	ep, err := epochFromKey(roomID, string(ptr))
	if err != nil {
		return 0, false, fmt.Errorf("%w: malformed latest pointer %q: %v", ErrCheckpointCorrupt, string(ptr), err)
	}
	return ep, true, nil
}

// epochFromKey parses the trailing /<epoch> segment of a checkpoint key for the
// given room, validating that the key is exactly the room's epoch key.
func epochFromKey(roomID, key string) (int64, error) {
	prefix := blobstore.CheckpointKey(roomID, 0)
	// CheckpointKey(roomID, 0) ends in the zero-padded epoch; strip to the room's
	// "snapshots/<roomID>/" prefix and parse what follows.
	base := strings.TrimSuffix(prefix, "00000000000000000000")
	if !strings.HasPrefix(key, base) {
		return 0, fmt.Errorf("key %q is not under room prefix %q", key, base)
	}
	rest := strings.TrimPrefix(key, base)
	if strings.Contains(rest, "/") {
		return 0, fmt.Errorf("key %q has extra path segments", key)
	}
	ep, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("epoch segment %q: %w", rest, err)
	}
	if ep < 0 {
		return 0, fmt.Errorf("negative epoch %d", ep)
	}
	return ep, nil
}
