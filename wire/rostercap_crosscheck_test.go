package wire

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestRustRosterCapMatchesWire is the crossverify-side guard for the roster
// ceiling: RosterCap is a protocol invariant (it bounds both the host's
// per-slot baseline cache and the guest's send-index validity), and the Rust
// guest SDK carries its own copy as broadcast.rs's ROSTER_CAP. The constant is
// pub(crate), so no compiled artifact exposes it; instead this test parses the
// Rust source directly and asserts it equals wire.RosterCap — runnable by
// plain `go test ./wire/...` with no Rust toolchain, alongside the byte-level
// crossverify golden vectors that guard the encodings themselves.
func TestRustRosterCapMatchesWire(t *testing.T) {
	path := filepath.Join("..", "rust", "src", "broadcast.rs")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Rust SDK source %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^pub\(crate\) const ROSTER_CAP: usize = (\d+);$`)
	matches := re.FindAllSubmatch(src, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one ROSTER_CAP declaration in %s, found %d — "+
			"if the declaration moved or was reworded, update this test's pattern "+
			"so the cross-SDK assertion keeps holding", path, len(matches))
	}
	got, err := strconv.Atoi(string(matches[0][1]))
	if err != nil {
		t.Fatalf("parsing ROSTER_CAP value %q: %v", matches[0][1], err)
	}
	if got != RosterCap {
		t.Fatalf("Rust SDK ROSTER_CAP = %d, wire.RosterCap = %d — these are one "+
			"protocol constant and must change in lockstep (see wire.RosterCap docs)",
			got, RosterCap)
	}
}
