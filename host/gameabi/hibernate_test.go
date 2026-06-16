package gameabi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/blobstore"
	"github.com/shellcade/kit/v2/host/sdk"
)

// hibernateHook returns the WithAbandonHibernate fn that the matchmaker wires in
// production: snapshot the live handler and park it in the store under roomID.
// It records the parked roster header so the test can assert membership.
func hibernateHook(t *testing.T, store *HibernationStore, slug, roomID string) func(sdk.Handler) error {
	t.Helper()
	return func(h sdk.Handler) error {
		blob, err := SnapshotHandler(h)
		if err != nil {
			return err
		}
		hdr := Header{Slug: slug, RoomID: roomID, At: time.Now()}
		if wh, ok := h.(*wasmHandler); ok {
			hdr.Roster = RosterFrom(wh.roster)
		}
		return store.Put(context.Background(), hdr, blob)
	}
}

// TestHeaderRoundTrip: the uncompressed header encodes and decodes without zstd,
// preserving every field, and reports the body offset so the snapshot body can be
// sliced off.
func TestHeaderRoundTrip(t *testing.T) {
	at := time.Unix(1_700_000_000, 123_456_789)
	in := Header{
		Slug:   "fixture",
		RoomID: "room-7",
		At:     at,
		Roster: []RosterMember{{AccountID: "a1", Handle: "ada"}, {AccountID: "a2", Handle: "bob"}},
	}
	body := []byte{0xde, 0xad, 0xbe, 0xef}
	blob := append(encodeHeader(in), body...)

	got, n, err := decodeHeader(blob)
	if err != nil {
		t.Fatalf("decodeHeader: %v", err)
	}
	if got.Slug != in.Slug || got.RoomID != in.RoomID || !got.At.Equal(in.At) {
		t.Fatalf("scalar fields differ: got %+v want %+v", got, in)
	}
	if len(got.Roster) != 2 || got.Roster[0] != in.Roster[0] || got.Roster[1] != in.Roster[1] {
		t.Fatalf("roster differs: %+v", got.Roster)
	}
	if !got.Has("a1") || got.Has("nope") {
		t.Fatalf("Has() membership wrong on %+v", got.Roster)
	}
	if string(blob[n:]) != string(body) {
		t.Fatalf("body offset wrong: blob[%d:]=%x want %x", n, blob[n:], body)
	}
}

// TestHeaderRejectsGarbage: decodeHeader must error (never panic) on truncated or
// bad-magic input — the store relies on this to skip foreign/corrupt objects.
func TestHeaderRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{nil, {0x00}, {0xff, 0xff, 0xff, 0xff}, make([]byte, 8)} {
		if _, _, err := decodeHeader(b); err == nil {
			t.Fatalf("decodeHeader(%x) accepted garbage", b)
		}
	}
}

// TestStoreListSkipsForeign: a non-header object under snapshots/ is skipped by
// List, not fatal — one bad blob must not hide valid parked rooms.
func TestStoreListSkipsForeign(t *testing.T) {
	mem := blobstore.NewMemory()
	store := NewHibernationStore(mem, nil)
	if err := store.Put(context.Background(), Header{Slug: "fixture", RoomID: "good", At: time.Now()}, []byte("body")); err != nil {
		t.Fatalf("put: %v", err)
	}
	// A foreign object directly in the bucket under the prefix.
	if err := mem.Put(context.Background(), snapshotPrefix+"junk", []byte("not a header")); err != nil {
		t.Fatalf("put junk: %v", err)
	}
	hdrs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hdrs) != 1 || hdrs[0].RoomID != "good" {
		t.Fatalf("list = %+v, want just the good header", hdrs)
	}
}

// TestSealedStoreRoundTrip: a store built with an HMAC Sealer parks a sealed
// blob and Get verifies + strips the seal, returning the exact header and body.
func TestSealedStoreRoundTrip(t *testing.T) {
	mem := blobstore.NewMemory()
	store := NewHibernationStore(mem, blobstore.NewHMACSealer([]byte("test-mac-key")))
	hdr := Header{Slug: "fixture", RoomID: "sealed-room", At: time.Unix(1_700_000_000, 0),
		Roster: []RosterMember{{AccountID: "a1", Handle: "ada"}}}
	body := []byte("opaque snapshot body")
	ctx := context.Background()
	if err := store.Put(ctx, hdr, body); err != nil {
		t.Fatalf("put: %v", err)
	}
	// The stored object must NOT be the raw header+body (the seal is real).
	raw, ok, err := mem.Get(ctx, snapshotPrefix+"sealed-room")
	if err != nil || !ok {
		t.Fatalf("raw get: ok=%v err=%v", ok, err)
	}
	if len(raw) == len(encodeHeader(hdr))+len(body) {
		t.Fatal("stored blob is exactly header+body — no MAC was appended")
	}
	got, gotBody, ok, err := store.Get(ctx, "sealed-room")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Slug != hdr.Slug || got.RoomID != hdr.RoomID || !got.Has("a1") {
		t.Fatalf("header round trip: %+v", got)
	}
	if string(gotBody) != string(body) {
		t.Fatalf("body round trip: %q", gotBody)
	}
}

// TestSealedStoreRejectsTamperAndUnsealed: a sealed store's Get must refuse —
// with blobstore.ErrSealVerify, before the header is even decoded — both a
// tampered blob and a legacy blob parked UNSEALED by a pre-sealing binary (the
// clean-cutover migration behavior). This is the write-primitive regression:
// nothing read from an unverified blob may reach the roster or guest memory.
func TestSealedStoreRejectsTamperAndUnsealed(t *testing.T) {
	mem := blobstore.NewMemory()
	sealer := blobstore.NewHMACSealer([]byte("test-mac-key"))
	sealed := NewHibernationStore(mem, sealer)
	ctx := context.Background()

	// Tampered: park sealed, then flip one byte of the stored object.
	hdr := Header{Slug: "fixture", RoomID: "tampered", At: time.Now()}
	if err := sealed.Put(ctx, hdr, []byte("body")); err != nil {
		t.Fatalf("put: %v", err)
	}
	raw, _, _ := mem.Get(ctx, snapshotPrefix+"tampered")
	raw[len(raw)/2] ^= 0x01
	if err := mem.Put(ctx, snapshotPrefix+"tampered", raw); err != nil {
		t.Fatalf("re-put tampered: %v", err)
	}
	if _, _, _, err := sealed.Get(ctx, "tampered"); !errors.Is(err, blobstore.ErrSealVerify) {
		t.Fatalf("tampered get err = %v, want ErrSealVerify", err)
	}

	// Unsealed legacy: parked by a store with no sealer (an older binary).
	legacy := NewHibernationStore(mem, nil)
	if err := legacy.Put(ctx, Header{Slug: "fixture", RoomID: "legacy", At: time.Now()}, []byte("body")); err != nil {
		t.Fatalf("legacy put: %v", err)
	}
	if _, _, _, err := sealed.Get(ctx, "legacy"); !errors.Is(err, blobstore.ErrSealVerify) {
		t.Fatalf("legacy get err = %v, want ErrSealVerify", err)
	}
}

// TestSealedListSkipsUnverified: the legacy List path (directory-less rigs)
// must verify each blob BEFORE decoding its header — the header roster gates
// Resume-list visibility, so tampered/unsealed/foreign objects are skipped.
func TestSealedListSkipsUnverified(t *testing.T) {
	mem := blobstore.NewMemory()
	sealer := blobstore.NewHMACSealer([]byte("test-mac-key"))
	sealed := NewHibernationStore(mem, sealer)
	ctx := context.Background()

	if err := sealed.Put(ctx, Header{Slug: "fixture", RoomID: "good", At: time.Now()}, []byte("body")); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Unsealed legacy blob with a VALID header and a forged roster: without
	// List-side verification this would surface in the forged member's menu.
	forged := append(encodeHeader(Header{Slug: "fixture", RoomID: "forged", At: time.Now(),
		Roster: []RosterMember{{AccountID: "victim", Handle: "v"}}}), "body"...)
	if err := mem.Put(ctx, snapshotPrefix+"forged", forged); err != nil {
		t.Fatalf("put forged: %v", err)
	}
	// Sealed-but-headerless object (a versioned checkpoint under the shared
	// prefix is sealed with the same key but carries no hibernation header).
	if err := mem.Put(ctx, snapshotPrefix+"room/00000000000000000001", sealer.Seal([]byte("checkpoint payload"))); err != nil {
		t.Fatalf("put checkpoint: %v", err)
	}

	hdrs, err := sealed.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hdrs) != 1 || hdrs[0].RoomID != "good" {
		t.Fatalf("list = %+v, want just the sealed good header", hdrs)
	}
}

// TestHibernateCapability: a wasm room with a wired hook reports Hibernatable;
// the same game without the hook does not (the room ends normally on
// abandonment, the legacy behavior).
func TestHibernateCapability(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	store := NewHibernationStore(blobstore.NewMemory(), nil)

	withHook := sdk.NewRoomRuntime("cap-hook", g.NewRoom(cfg, sdk.Services{Log: quietLog()}), cfg, sdk.Services{Log: quietLog()},
		sdk.WithAbandonHibernate(hibernateHook(t, store, "fixture", "cap-hook"), time.Hour))
	t.Cleanup(func() { _ = withHook.Close() })
	if err := withHook.Join(p1); err != nil {
		t.Fatalf("join: %v", err)
	}
	waitFor(t, "hooked room hibernatable", func() bool { return withHook.Hibernatable() })

	noHook := newLiveRoom(t, g, cfg)
	if err := noHook.Join(p1); err != nil {
		t.Fatalf("join: %v", err)
	}
	// Give it a beat to start; it must never report hibernatable without a hook.
	time.Sleep(20 * time.Millisecond)
	if noHook.Hibernatable() {
		t.Fatal("room without a hibernate hook reported Hibernatable")
	}
}

// TestHibernateDrainTrigger drives the deploy-drain path: a live room with a
// member is frozen via RoomCtl.Hibernate. The snapshot lands in the store with
// the room's roster, the player's frame stream closes (paused, not finished),
// the room reports Done, and crucially NO Result is published (a hibernated room
// is not a finished room — no DNF backfill, no leaderboard post).
func TestHibernateDrainTrigger(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	store := NewHibernationStore(blobstore.NewMemory(), nil)
	roomID := "drain-1"
	ctl := sdk.NewRoomRuntime(roomID, g.NewRoom(cfg, sdk.Services{Log: quietLog()}), cfg, sdk.Services{Log: quietLog()},
		sdk.WithAbandonHibernate(hibernateHook(t, store, "fixture", roomID), time.Hour))
	t.Cleanup(func() { _ = ctl.Close() })

	if err := ctl.Join(p1); err != nil {
		t.Fatalf("join: %v", err)
	}
	frames := ctl.Frames(p1)
	waitFor(t, "hibernatable", func() bool { return ctl.Hibernatable() })

	if err := ctl.Hibernate(func(h sdk.Handler) error {
		return hibernateHook(t, store, "fixture", roomID)(h)
	}); err != nil {
		t.Fatalf("hibernate: %v", err)
	}

	// The room is disposed: Done closes, the player stream closes.
	select {
	case <-ctl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("room did not dispose after hibernate")
	}
	select {
	case _, ok := <-frames:
		if ok {
			// drain any buffered frame, then confirm the stream is now closed
			select {
			case _, ok2 := <-frames:
				if ok2 {
					t.Fatal("player frame stream still open after hibernate")
				}
			case <-time.After(time.Second):
				t.Fatal("player frame stream did not close after hibernate")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("player frame stream did not close after hibernate")
	}

	// No Result was published: hibernation is a pause, not an end.
	if res, ok := ctl.Result(); ok {
		t.Fatalf("hibernated room published a Result: %+v", res)
	}

	// The snapshot is parked, tagged with the room's member.
	hdrs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hdrs) != 1 {
		t.Fatalf("parked %d snapshots, want 1", len(hdrs))
	}
	if hdrs[0].RoomID != roomID || hdrs[0].Slug != "fixture" || !hdrs[0].Has(p1.AccountID) {
		t.Fatalf("header = %+v, want room=%s slug=fixture with p1", hdrs[0], roomID)
	}

	// The body restores into a fresh handler with the right roster.
	_, body, ok, err := store.Get(context.Background(), roomID)
	if err != nil || !ok {
		t.Fatalf("get body: ok=%v err=%v", ok, err)
	}
	h, err := RestoreHandler(g, body)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	wh := h.(*wasmHandler)
	if len(wh.roster) != 1 || wh.roster[0] != p1 {
		t.Fatalf("restored roster = %+v, want [p1]", wh.roster)
	}
}

// TestHibernateAbandonTrigger drives the abandonment path: an empty
// hibernate-capable room is NOT ended immediately; after the grace window it
// auto-hibernates instead. (A non-capable room ends right away — covered by the
// existing room tests.)
func TestHibernateAbandonTrigger(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	store := NewHibernationStore(blobstore.NewMemory(), nil)
	roomID := "abandon-1"
	ctl := sdk.NewRoomRuntime(roomID, g.NewRoom(cfg, sdk.Services{Log: quietLog()}), cfg, sdk.Services{Log: quietLog()},
		sdk.WithAbandonHibernate(hibernateHook(t, store, "fixture", roomID), 60*time.Millisecond))
	t.Cleanup(func() { _ = ctl.Close() })

	if err := ctl.Join(p1); err != nil {
		t.Fatalf("join: %v", err)
	}
	waitFor(t, "hibernatable", func() bool { return ctl.Hibernatable() })

	// Member leaves: the room must NOT settle immediately (grace reprieve).
	ctl.Leave(p1)
	time.Sleep(20 * time.Millisecond)
	select {
	case <-ctl.Done():
		t.Fatal("room disposed before the grace window elapsed")
	default:
	}

	// After the grace window the room auto-hibernates: Done, snapshot parked.
	select {
	case <-ctl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("room did not auto-hibernate after the grace window")
	}
	if res, ok := ctl.Result(); ok {
		t.Fatalf("abandoned-then-hibernated room published a Result: %+v", res)
	}
	hdrs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hdrs) != 1 || hdrs[0].RoomID != roomID {
		t.Fatalf("parked headers = %+v, want one for %s", hdrs, roomID)
	}
}

// TestHibernateGraceCancelledByRejoin: a rejoin inside the grace window cancels
// the pending abandonment hibernation — the room stays live and nothing is
// parked.
func TestHibernateGraceCancelledByRejoin(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 2, MinPlayers: 1, Seed: 7, SeedSet: true}
	store := NewHibernationStore(blobstore.NewMemory(), nil)
	roomID := "rejoin-1"
	ctl := sdk.NewRoomRuntime(roomID, g.NewRoom(cfg, sdk.Services{Log: quietLog()}), cfg, sdk.Services{Log: quietLog()},
		sdk.WithAbandonHibernate(hibernateHook(t, store, "fixture", roomID), 150*time.Millisecond))
	t.Cleanup(func() { _ = ctl.Close() })

	if err := ctl.Join(p1); err != nil {
		t.Fatalf("join: %v", err)
	}
	waitFor(t, "hibernatable", func() bool { return ctl.Hibernatable() })

	ctl.Leave(p1) // empties the room, arms the grace timer
	time.Sleep(30 * time.Millisecond)
	if err := ctl.Join(p2); err != nil { // rejoin before grace fires
		t.Fatalf("rejoin: %v", err)
	}

	// Wait past the original grace window; the room must still be live.
	time.Sleep(250 * time.Millisecond)
	select {
	case <-ctl.Done():
		t.Fatal("room hibernated despite a rejoin inside the grace window")
	default:
	}
	hdrs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hdrs) != 0 {
		t.Fatalf("parked %d snapshots after a cancelling rejoin, want 0", len(hdrs))
	}
}
