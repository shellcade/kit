package blobstore

import (
	"bytes"
	"context"
	"testing"
)

func testStore(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	// Missing key: ok=false, no error.
	if _, ok, err := s.Get(ctx, "artifacts/missing.wasm"); ok || err != nil {
		t.Fatalf("Get missing = ok=%v err=%v, want false nil", ok, err)
	}

	// Put / Get round-trip.
	blob := []byte("\x00asm fake artifact bytes")
	if err := s.Put(ctx, "artifacts/abc.wasm", blob); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := s.Get(ctx, "artifacts/abc.wasm")
	if err != nil || !ok || !bytes.Equal(got, blob) {
		t.Fatalf("Get = %q ok=%v err=%v, want round-trip", got, ok, err)
	}

	// Overwrite wins.
	if err := s.Put(ctx, "artifacts/abc.wasm", []byte("v2")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	if got, _, _ := s.Get(ctx, "artifacts/abc.wasm"); string(got) != "v2" {
		t.Fatalf("Get after overwrite = %q, want v2", got)
	}

	// List by prefix, lexical order.
	for _, k := range []string{"snapshots/room-2", "snapshots/room-1", "artifacts/zzz.wasm"} {
		if err := s.Put(ctx, k, []byte(k)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	keys, err := s.List(ctx, "snapshots/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"snapshots/room-1", "snapshots/room-2"}
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("List = %v, want %v", keys, want)
	}

	// Delete: gone after; deleting missing is not an error.
	if err := s.Delete(ctx, "snapshots/room-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "snapshots/room-1"); ok {
		t.Fatal("deleted key still readable")
	}
	if err := s.Delete(ctx, "snapshots/never-existed"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	testStore(t, NewMemory())
}

func TestDirStore(t *testing.T) {
	s, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	testStore(t, s)
}

// TestDirStorePersistsAcrossReopen is the property the dev hibernation demo
// rides on: a snapshot written by one Dir (one `serve` run) is readable by a
// fresh Dir over the same root (the restarted `serve`). It also covers a
// slash-bearing slug key (snapshots/<author>/<name>-<seq>) becoming nested
// directories on disk.
func TestDirStorePersistsAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	first, err := NewDir(root)
	if err != nil {
		t.Fatalf("NewDir first: %v", err)
	}
	key := "snapshots/dev/fixture-1"
	blob := []byte("hibernated room state")
	if err := first.Put(ctx, key, blob); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Restart: a brand-new store over the same directory must see the snapshot.
	second, err := NewDir(root)
	if err != nil {
		t.Fatalf("NewDir second: %v", err)
	}
	got, ok, err := second.Get(ctx, key)
	if err != nil || !ok || !bytes.Equal(got, blob) {
		t.Fatalf("Get after reopen = %q ok=%v err=%v, want round-trip", got, ok, err)
	}
	keys, err := second.List(ctx, "snapshots/")
	if err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("List after reopen = %v err=%v, want [%s]", keys, err, key)
	}
}

// TestDirStoreRejectsEscape: a key with .. must not escape the root.
func TestDirStoreRejectsEscape(t *testing.T) {
	s, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	if err := s.Put(context.Background(), "../escape", []byte("x")); err == nil {
		t.Fatal("Put with escaping key succeeded, want error")
	}
}

// TestSlashSlugSnapshotKeys proves the blob store handles keys built from a
// namespaced room id, e.g. snapshots/<slug>-<seq> where the slug itself is
// <author>/<name> ("bcook/pokies"). The resulting key carries an extra slash
// segment ("snapshots/bcook/pokies-1"); it must still round-trip and surface
// under both the broad "snapshots/" prefix and the narrower per-author prefix.
func TestSlashSlugSnapshotKeys(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()

	// Room ids for two namespaced games: "<slug>-<seq>".
	keyA := "snapshots/bcook/pokies-1"
	keyB := "snapshots/alan/chess-3"
	for _, k := range []string{keyA, keyB} {
		if err := s.Put(ctx, k, []byte(k)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got, ok, err := s.Get(ctx, keyA)
	if err != nil || !ok || !bytes.Equal(got, []byte(keyA)) {
		t.Fatalf("Get %s = %q ok=%v err=%v, want round-trip", keyA, got, ok, err)
	}

	// The boot-time TTL rule expires everything under "snapshots/"; a slash slug
	// must not escape that prefix.
	keys, err := s.List(ctx, "snapshots/")
	if err != nil {
		t.Fatalf("List snapshots/: %v", err)
	}
	want := []string{keyB, keyA} // lexical: alan/ before bcook/
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("List(snapshots/) = %v, want %v", keys, want)
	}

	// A per-author prefix selects only that author's snapshots.
	bcook, err := s.List(ctx, "snapshots/bcook/")
	if err != nil {
		t.Fatalf("List snapshots/bcook/: %v", err)
	}
	if len(bcook) != 1 || bcook[0] != keyA {
		t.Fatalf("List(snapshots/bcook/) = %v, want [%s]", bcook, keyA)
	}

	// Delete-on-restore removes exactly the one snapshot.
	if err := s.Delete(ctx, keyA); err != nil {
		t.Fatalf("Delete %s: %v", keyA, err)
	}
	if _, ok, _ := s.Get(ctx, keyA); ok {
		t.Fatalf("%s still readable after delete", keyA)
	}
	if _, ok, _ := s.Get(ctx, keyB); !ok {
		t.Fatalf("%s wrongly removed when deleting %s", keyB, keyA)
	}
}
