package gameabi

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/blobstore"
	"github.com/shellcade/kit/v2/host/sdk"
)

// wakesOf parses the fixture's "wakes=N" status row (row 2) from a frame.
func wakesOf(t *testing.T, f sdk.Frame) int {
	t.Helper()
	row := rowText(f, 2)
	if !strings.HasPrefix(row, "wakes=") {
		t.Fatalf("frame row 2 = %q, want wakes=N", row)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(row, "wakes="))
	if err != nil {
		t.Fatalf("bad wakes row %q: %v", row, err)
	}
	return n
}

// TestHibernateE2EContinuity is the full hibernation round trip across a process
// restart boundary, exercised through the real engine + the hibernation store:
//
//  1. A live wasm room (the matchmaker's runtime) plays — a member joins and the
//     heartbeat drives wakes, advancing the guest's persistent wake counter.
//  2. The room is hibernated via the drain path (RoomCtl.Hibernate), which
//     snapshots the live handler and parks it in the store over a Memory
//     blobstore. The room disposes WITHOUT a result.
//  3. We simulate a restart: a FRESH LoadGame (new CompiledPlugin) and a FRESH
//     HibernationStore handle over the SAME Memory blobstore — nothing carried in
//     process memory but the blob bytes.
//  4. We restore and continue: the restored handler's wake counter picks up from
//     where it left off (wakes=N -> N+1), proving guest memory survived; and the
//     snapshot is deleted from the store on a successful restore.
func TestHibernateE2EContinuity(t *testing.T) {
	mem := blobstore.NewMemory() // the durable bucket that survives the "restart"

	// --- phase 1+2: play a live room, then hibernate it via the drain path ---
	gLive := loadFixture(t, Options{Heartbeat: 25 * time.Millisecond})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 99, SeedSet: true}
	storeA := NewHibernationStore(mem, nil)
	roomID := "e2e-room"
	ctl := sdk.NewRoomRuntime(roomID, gLive.NewRoom(cfg, sdk.Services{Log: quietLog()}), cfg, sdk.Services{Log: quietLog()},
		sdk.WithAbandonHibernate(hibernateHook(t, storeA, "fixture", roomID), time.Hour))
	t.Cleanup(func() { _ = ctl.Close() })

	if err := ctl.Join(p1); err != nil {
		t.Fatalf("join: %v", err)
	}
	frames := ctl.Frames(p1)

	// Let the heartbeat advance the wake counter past zero, then capture the last
	// observed wakes value from a broadcast frame.
	var lastWakes int
	waitFor(t, "some wakes", func() bool {
		f, ok := drainLatest(frames)
		if !ok {
			return false
		}
		lastWakes = wakesOf(t, f)
		return lastWakes >= 2
	})

	// Hibernate via the drain path. The snapshot is taken at a quiescent point on
	// the actor, so it captures a coherent wake counter >= the last we saw.
	if err := ctl.Hibernate(hibernateHook(t, storeA, "fixture", roomID)); err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	select {
	case <-ctl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("room did not dispose after drain hibernate")
	}
	if _, ok := ctl.Result(); ok {
		t.Fatal("hibernated room published a Result (should be paused, not finished)")
	}

	// --- phase 3: simulate a restart — fresh game + fresh store, same bucket ---
	gFresh := loadFixture(t, Options{Heartbeat: 25 * time.Millisecond})
	if gFresh.(*wasmGame) == gLive.(*wasmGame) {
		t.Fatal("expected a distinct CompiledPlugin after reload")
	}
	storeB := NewHibernationStore(mem, nil)

	hdrs, err := storeB.List(context.Background())
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if len(hdrs) != 1 || hdrs[0].RoomID != roomID {
		t.Fatalf("parked headers after restart = %+v, want one for %s", hdrs, roomID)
	}

	hdr, body, ok, err := storeB.Get(context.Background(), roomID)
	if err != nil || !ok {
		t.Fatalf("get parked room: ok=%v err=%v", ok, err)
	}
	if hdr.Slug != "fixture" {
		t.Fatalf("header slug = %q, want fixture", hdr.Slug)
	}

	// --- phase 4: restore + continue, assert wake continuity + snapshot gone ---
	h, err := RestoreHandler(gFresh, body)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	wh := h.(*wasmHandler)
	snapWakes := guestWakes(t, wh)
	if snapWakes < lastWakes {
		t.Fatalf("restored wakes=%d < last observed %d (state regressed)", snapWakes, lastWakes)
	}

	// One more wake on the restored handler must advance from the restored value:
	// continuity across the hibernate boundary.
	r := newReplayRoom([]sdk.Player{p1}, cfg, time.Unix(1_700_000_000, 0))
	wh.OnTick(r, r.clock)
	got := wakesOf(t, r.frames[len(r.frames)-1])
	if got != snapWakes+1 {
		t.Fatalf("post-restore wake produced wakes=%d, want %d (continuity broken)", got, snapWakes+1)
	}

	// Snapshot deleted on successful restore (delete-on-restore), so a resume can
	// never double-spawn the same room.
	if err := storeB.Delete(context.Background(), roomID); err != nil {
		t.Fatalf("delete on restore: %v", err)
	}
	hdrs, err = storeB.List(context.Background())
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(hdrs) != 0 {
		t.Fatalf("snapshot still present after restore+delete: %+v", hdrs)
	}
}

// guestWakes reads the restored handler's current wake counter via a wake-free
// render ('x' is unhandled -> render path; no wake increment). In ABI v2 the
// FIRST post-restore send hits the resync: the host's baseline cache is
// ephemeral (not snapshotted) so it re-seeds the epoch and rejects the restored
// guest's delta — and the SDK retries the same frame as a keyframe within the
// same call (kit >= v2.0.1), so one render suffices and no frame is dropped.
func guestWakes(t *testing.T, wh *wasmHandler) int {
	t.Helper()
	r := newReplayRoom([]sdk.Player{p1}, wh.cfg, time.Unix(1_700_000_000, 0))
	wh.OnResume(r)                 // engine resume path: re-seed epoch, mark slots not-present
	wh.OnInput(r, p1, runeIn('x')) // post-restore render: delta rejected, keyframe retried in-call
	if len(r.frames) == 0 {
		t.Fatal("restored handler produced no frame after the resync render")
	}
	return wakesOf(t, r.frames[len(r.frames)-1])
}

// drainLatest returns the most recent buffered frame without blocking (ok=false
// if none is buffered).
func drainLatest(ch <-chan sdk.Frame) (sdk.Frame, bool) {
	var last sdk.Frame
	got := false
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				return last, got
			}
			last, got = f, true
		default:
			return last, got
		}
	}
}

// TestHibernateFailedRestoreDiscards: a corrupt / artifact-mismatched snapshot
// cannot be resumed, and the safe-degrade path discards it so a member never
// gets stuck staring at a dead resume entry. Here we restore against the WRONG
// game (a freshly loaded fixture is fine, but a tampered body fails the codec's
// artifact-hash check); on failure the caller discards via Delete.
func TestHibernateFailedRestoreDiscards(t *testing.T) {
	mem := blobstore.NewMemory()
	store := NewHibernationStore(mem, nil)
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	roomID := "corrupt-room"

	// Park a valid snapshot, then corrupt its body in the bucket so restore fails.
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	rr := newReplayRoom([]sdk.Player{p1}, cfg, time.Unix(1_700_000_000, 0))
	h.OnStart(rr)
	h.OnJoin(rr, p1)
	blob, err := SnapshotHandler(h)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := store.Put(context.Background(), Header{Slug: "fixture", RoomID: roomID, At: time.Now(), Roster: RosterFrom([]sdk.Player{p1})}, blob); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Corrupt the stored body (flip a byte deep in the zstd payload).
	_, body, ok, err := store.Get(context.Background(), roomID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	body[len(body)/2] ^= 0xff
	if err := store.Put(context.Background(), Header{Slug: "fixture", RoomID: roomID, At: time.Now(), Roster: RosterFrom([]sdk.Player{p1})}, body); err != nil {
		t.Fatalf("re-put corrupt: %v", err)
	}

	// Restore must fail (corrupt body), and the resume flow discards the snapshot.
	_, badBody, ok, err := store.Get(context.Background(), roomID)
	if err != nil || !ok {
		t.Fatalf("re-get: ok=%v err=%v", ok, err)
	}
	if _, err := RestoreHandler(g, badBody); err == nil {
		t.Fatal("RestoreHandler accepted a corrupt snapshot body")
	}
	// Safe-degrade: discard the un-restorable snapshot.
	if err := store.Delete(context.Background(), roomID); err != nil {
		t.Fatalf("discard: %v", err)
	}
	hdrs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hdrs) != 0 {
		t.Fatalf("corrupt snapshot not discarded: %+v", hdrs)
	}
}
