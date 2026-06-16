package gameabi

import (
	"context"
	"errors"
	"testing"

	"github.com/shellcade/kit/v2/host/blobstore"
)

const ckRoom = "0190b8a0-1234-7abc-8def-0123456789ab"

func newCkStore() (*CheckpointStore, *blobstore.Memory) {
	mem := blobstore.NewMemory()
	cs := NewCheckpointStore(mem, blobstore.NewHMACSealer([]byte("server-side-key")))
	return cs, mem
}

// Write then ReadLatest round-trips the payload and reports the epoch the
// latest pointer names.
func TestCheckpointWriteReadLatest(t *testing.T) {
	cs, _ := newCkStore()
	ctx := context.Background()
	payload := []byte("room checkpoint bytes")
	if err := cs.Write(ctx, ckRoom, 7, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, epoch, err := cs.ReadLatest(ctx, ckRoom)
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if epoch != 7 {
		t.Errorf("ReadLatest epoch = %d, want 7", epoch)
	}
	if string(got) != string(payload) {
		t.Errorf("ReadLatest payload = %q, want %q", got, payload)
	}
}

// ReadLatest with no checkpoint reports not-found (ok=false-style: a sentinel
// error the caller can distinguish from corruption).
func TestCheckpointReadLatestMissing(t *testing.T) {
	cs, _ := newCkStore()
	_, _, err := cs.ReadLatest(context.Background(), ckRoom)
	if !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("ReadLatest(missing) err = %v, want ErrNoCheckpoint", err)
	}
}

// A checkpoint key is never overwritten in place: a second Write at the same
// epoch keeps the FIRST object (under the single-writer-per-room precondition
// it can only be our own interrupted earlier attempt) and converges — it
// succeeds and finishes the pointer advance rather than failing forever.
func TestCheckpointNeverOverwriteEpoch(t *testing.T) {
	cs, _ := newCkStore()
	ctx := context.Background()
	if err := cs.Write(ctx, ckRoom, 3, []byte("first")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if err := cs.Write(ctx, ckRoom, 3, []byte("second")); err != nil {
		t.Fatalf("Write 2 (same epoch) must resume, not fail: %v", err)
	}
	// The original payload survives: the retry must not overwrite in place.
	got, epoch, err := cs.ReadLatest(ctx, ckRoom)
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if epoch != 3 {
		t.Errorf("latest epoch = %d, want 3", epoch)
	}
	if string(got) != "first" {
		t.Errorf("payload = %q, want %q (overwrite must not land)", got, "first")
	}
}

// pointerFailOnce wraps the in-memory store to fail the FIRST latest-pointer
// PUT — the interrupted-fire shape: the epoch object lands, the pointer advance
// does not (a per-fire timeout or a store blip between the two PUTs).
type pointerFailOnce struct {
	*blobstore.Memory
	failed bool
}

func (p *pointerFailOnce) Put(ctx context.Context, key string, data []byte) error {
	if !p.failed && key == blobstore.LatestPointerKey(ckRoom) {
		p.failed = true
		return errors.New("injected: pointer put failed")
	}
	return p.Memory.Put(ctx, key, data)
}

// The retry-same-epoch contract the cadence depends on (CheckpointScheduler
// retries an unadvanced epoch next interval; the drain fires at NextEpoch ==
// that same epoch): a Write interrupted AFTER its epoch object landed but
// BEFORE the pointer advanced must converge on retry — resume from the stored
// object and finish the pointer advance — not fail "already written" forever.
func TestCheckpointInterruptedWriteRetryConverges(t *testing.T) {
	mem := &pointerFailOnce{Memory: blobstore.NewMemory()}
	cs := NewCheckpointStore(mem, blobstore.NewHMACSealer([]byte("server-side-key")))
	ctx := context.Background()

	if err := cs.Write(ctx, ckRoom, 5, []byte("captured")); err == nil {
		t.Fatal("first Write should fail (injected pointer-put failure)")
	}
	// The epoch object landed; the pointer did not.
	if _, _, err := cs.ReadLatest(ctx, ckRoom); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("pointer must not have advanced, ReadLatest err = %v", err)
	}

	// The scheduler retries the SAME epoch (it advances only on success).
	if err := cs.Write(ctx, ckRoom, 5, []byte("recaptured")); err != nil {
		t.Fatalf("retry at the same epoch must converge: %v", err)
	}
	got, epoch, err := cs.ReadLatest(ctx, ckRoom)
	if err != nil {
		t.Fatalf("ReadLatest after retry: %v", err)
	}
	if epoch != 5 {
		t.Errorf("latest epoch = %d, want 5", epoch)
	}
	// The FIRST attempt's complete object is what the pointer names (never
	// overwritten in place); it is a valid sealed snapshot, one capture older.
	if string(got) != "captured" {
		t.Errorf("payload = %q, want %q (the interrupted attempt's object)", got, "captured")
	}
}

// The drain scenario, single-writer (the realistic case): a higher-epoch Write
// completes and swaps the pointer, THEN a lower-epoch Write completes. This
// proves the contract — a lower-epoch Write that completes AFTER a higher-epoch
// Write cannot regress the pointer — at the Write granularity the per-room actor
// guarantees. It deliberately does NOT interleave the two Writes' internal
// read-check-PUT steps: concurrent writers to one room are out of contract (see
// the single-writer-per-room precondition on CheckpointStore), so there is
// nothing finer to assert here.
func TestCheckpointLatePutCannotClobber(t *testing.T) {
	cs, _ := newCkStore()
	ctx := context.Background()
	// Newer (drain) epoch lands first and swaps the pointer.
	if err := cs.Write(ctx, ckRoom, 10, []byte("drain")); err != nil {
		t.Fatalf("Write drain: %v", err)
	}
	// Slow periodic PUT at a LOWER epoch completes afterward.
	if err := cs.Write(ctx, ckRoom, 9, []byte("periodic")); err != nil {
		t.Fatalf("Write periodic: %v", err)
	}
	got, epoch, err := cs.ReadLatest(ctx, ckRoom)
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if epoch != 10 {
		t.Errorf("latest epoch = %d, want 10 (drain must remain latest)", epoch)
	}
	if string(got) != "drain" {
		t.Errorf("latest payload = %q, want %q", got, "drain")
	}
}

// A blob tampered IN STORAGE is refused before any payload byte reaches the
// caller, mapped to the quarantine-able ErrCheckpointCorrupt (wrapping
// ErrSealVerify).
func TestCheckpointTamperedInStorageRefused(t *testing.T) {
	cs, mem := newCkStore()
	ctx := context.Background()
	if err := cs.Write(ctx, ckRoom, 1, []byte("good payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Mutate the stored object directly via the in-memory double.
	key := blobstore.CheckpointKey(ckRoom, 1)
	stored, ok, _ := mem.Get(ctx, key)
	if !ok {
		t.Fatal("checkpoint object not in store")
	}
	stored[0] ^= 0xff
	if err := mem.Put(ctx, key, stored); err != nil {
		t.Fatalf("Put tampered: %v", err)
	}

	payload, _, err := cs.ReadLatest(ctx, ckRoom)
	if !errors.Is(err, ErrCheckpointCorrupt) {
		t.Fatalf("ReadLatest(tampered) err = %v, want ErrCheckpointCorrupt", err)
	}
	if !errors.Is(err, blobstore.ErrSealVerify) {
		t.Fatalf("ErrCheckpointCorrupt must wrap ErrSealVerify, got %v", err)
	}
	if payload != nil {
		t.Fatalf("no payload may escape a corrupt checkpoint, got %q", payload)
	}
}

// A checkpoint sealed under a different server-side key is refused (artifact
// equality is not blob integrity; the key is).
func TestCheckpointWrongKeyRefused(t *testing.T) {
	mem := blobstore.NewMemory()
	writer := NewCheckpointStore(mem, blobstore.NewHMACSealer([]byte("key-a")))
	reader := NewCheckpointStore(mem, blobstore.NewHMACSealer([]byte("key-b")))
	ctx := context.Background()
	if err := writer.Write(ctx, ckRoom, 1, []byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	payload, _, err := reader.ReadLatest(ctx, ckRoom)
	if !errors.Is(err, ErrCheckpointCorrupt) {
		t.Fatalf("ReadLatest(wrong key) err = %v, want ErrCheckpointCorrupt", err)
	}
	if payload != nil {
		t.Fatalf("no payload may escape, got %q", payload)
	}
}

// nil store / sealer are programmer errors guarded with an error, matching
// HibernationStore's not-configured guard.
func TestCheckpointStoreNotConfigured(t *testing.T) {
	var cs *CheckpointStore
	if err := cs.Write(context.Background(), ckRoom, 0, nil); err == nil {
		t.Fatal("Write on nil store should error")
	}
	if _, _, err := cs.ReadLatest(context.Background(), ckRoom); err == nil {
		t.Fatal("ReadLatest on nil store should error")
	}
}
