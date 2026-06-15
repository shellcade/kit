package sdk

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// NewRoomID mints distinct ids across many calls, including across simulated
// process restarts (two independent batches must not collide) — the directory
// PK and checkpoint prefix depend on no cross-machine/cross-restart counter.
func TestNewRoomIDUnique(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, 2*n)
	mint := func() {
		for i := 0; i < n; i++ {
			id := NewRoomID()
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate room id %q", id)
			}
			seen[id] = struct{}{}
		}
	}
	mint() // batch one
	mint() // batch two — stands in for a fresh process
	if len(seen) != 2*n {
		t.Fatalf("got %d distinct ids, want %d", len(seen), 2*n)
	}
}

// ValidRoomID accepts canonical v7 mints and rejects the legacy "<slug>-<seq>"
// id format, wrong-version UUIDs, and non-canonical UUID spellings (the room id
// is a directory primary key, so only the exact canonical form is valid).
func TestValidRoomID(t *testing.T) {
	canonical := NewRoomID()
	if !ValidRoomID(canonical) {
		t.Fatalf("ValidRoomID(%q) = false, want true for a fresh mint", canonical)
	}
	for _, bad := range []string{
		"type-racer-1",
		"preview-type-racer-2",
		"",
		"not-a-uuid",
		// A valid UUID of the wrong version (v4) is not a room id.
		uuid.New().String(),
		// Non-canonical forms uuid.Parse accepts but a PK must not.
		"{" + canonical + "}",                  // braced
		"urn:uuid:" + canonical,                // urn-prefixed
		strings.ReplaceAll(canonical, "-", ""), // undashed
		strings.ToUpper(canonical),             // upper-case hex
	} {
		if ValidRoomID(bad) {
			t.Fatalf("ValidRoomID(%q) = true, want false", bad)
		}
	}
}

// V7 ids are time-ordered: ids minted in sequence sort by creation time, so the
// directory PK is index-friendly.
func TestNewRoomIDTimeOrdered(t *testing.T) {
	const n = 256
	ids := make([]string, n)
	for i := range ids {
		ids[i] = NewRoomID()
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i := range ids {
		if ids[i] != sorted[i] {
			t.Fatalf("ids not time-ordered: position %d is %q in mint order but %q when sorted", i, ids[i], sorted[i])
		}
	}
}
