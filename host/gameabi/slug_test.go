package gameabi

import (
	"strings"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// stubGame is a registry entry carrying an arbitrary (here: namespaced) slug,
// so the quarantine watchdog can be exercised without a wasm artifact — it keys
// purely on the registry slug string.
type stubGame struct {
	sdk.GameBase
	slug string
}

func (g stubGame) Meta() sdk.GameMeta {
	return sdk.GameMeta{Slug: g.slug, Name: g.slug, MinPlayers: 1, MaxPlayers: 2}
}
func (stubGame) NewRoom(cfg sdk.RoomConfig, svc sdk.Services) sdk.Handler { return nil }

// TestQuarantineKeysOnSlashSlug proves the fault-count watchdog keys on the
// full namespaced slug (a plain map key): faults under "bcook/pokies" remove
// exactly that game and Restore brings it back — a slash in the slug is inert.
func TestQuarantineKeysOnSlashSlug(t *testing.T) {
	const slug = "bcook/pokies"
	reg := sdk.NewRegistry()
	reg.MustAdd(stubGame{slug: slug})
	q := NewQuarantine(reg, 2, time.Minute, quietLog())
	clock := time.Unix(1_700_000_000, 0)
	q.now = func() time.Time { return clock }

	q.RecordFault(slug)
	q.RecordFault(slug) // threshold reached within the window
	if _, ok := reg.Get(slug); ok {
		t.Fatalf("slash slug %q still in roster after threshold faults", slug)
	}
	if qs := q.Quarantined(); len(qs) != 1 || qs[0] != slug {
		t.Fatalf("Quarantined() = %v, want [%s]", qs, slug)
	}
	if err := q.Restore(slug); err != nil {
		t.Fatalf("Restore(%q): %v", slug, err)
	}
	if _, ok := reg.Get(slug); !ok {
		t.Fatalf("restored slash slug %q missing from roster", slug)
	}
}

// TestValidateBareName proves LoadGame's meta gate: a guest may declare ONLY a
// bare game name; the host composes the <author>/<name> namespace, so a binary
// can never ship a slug that claims one. This is the unit under the LoadGame
// rejection (decodeMeta -> validateBareName), tested directly so it needs no
// hand-rolled malformed wasm artifact.
func TestValidateBareName(t *testing.T) {
	valid := []string{
		"fixture",
		"pokies",
		"shellracer",
		"a",
		"chess",
		"2048",
		strings.Repeat("a", 32), // exactly the length cap
	}
	for _, s := range valid {
		if err := validateBareName(s); err != nil {
			t.Errorf("validateBareName(%q) = %v, want nil (a bare name must load)", s, err)
		}
	}

	invalid := []string{
		"bcook/pokies",          // a namespaced slug: the host owns the prefix
		"alan/chess",            // ditto
		"a/b/c",                 // multiple separators
		"/pokies",               // leading slash
		"pokies/",               // trailing slash
		"Pokies",                // upper-case
		"type_racer",            // underscore is not in the bare-name alphabet
		"my game",               // space
		"poké",                  // non-ASCII
		"",                      // empty (also caught by wire, belt-and-suspenders)
		strings.Repeat("a", 33), // over the 32-rune cap
		"name@1",                // punctuation
	}
	for _, s := range invalid {
		if err := validateBareName(s); err == nil {
			t.Errorf("validateBareName(%q) = nil, want a rejection (only bare names are allowed)", s)
		}
	}
}

// TestValidateBareNameErrorIsActionable ensures the rejection names the slug and
// explains that the host composes the namespace — an author debugging a failed
// load gets a clear cause, not a bare regex mismatch.
func TestValidateBareNameErrorIsActionable(t *testing.T) {
	err := validateBareName("bcook/pokies")
	if err == nil {
		t.Fatal("expected an error for a namespaced guest slug")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bcook/pokies") {
		t.Errorf("error %q should echo the offending slug", msg)
	}
	if !strings.Contains(msg, "bare name") {
		t.Errorf("error %q should explain the bare-name rule", msg)
	}
}
