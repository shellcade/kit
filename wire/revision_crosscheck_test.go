package wire

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestRustWireRevisionMatchesWire is the crossverify-side guard for the wire
// revision: Revision is a protocol constant (it is what an artifact's meta
// declares as the newest wire feature it may assume, and what a host compares
// its own compiled-in revision against), and the Rust guest SDK carries its
// own copy as wire.rs's WIRE_REVISION. The constant is pub(crate), so no
// compiled artifact exposes it; instead this test parses the Rust source
// directly and asserts it equals wire.Revision — runnable by plain
// `go test ./wire/...` with no Rust toolchain, alongside the byte-level
// crossverify golden vectors that guard the encodings themselves.
func TestRustWireRevisionMatchesWire(t *testing.T) {
	path := filepath.Join("..", "rust", "src", "wire.rs")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Rust SDK source %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^pub\(crate\) const WIRE_REVISION: u16 = (\d+);$`)
	matches := re.FindAllSubmatch(src, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one WIRE_REVISION declaration in %s, found %d — "+
			"if the declaration moved or was reworded, update this test's pattern "+
			"so the cross-SDK assertion keeps holding", path, len(matches))
	}
	got, err := strconv.Atoi(string(matches[0][1]))
	if err != nil {
		t.Fatalf("parsing WIRE_REVISION value %q: %v", matches[0][1], err)
	}
	if got != int(Revision) {
		t.Fatalf("Rust SDK WIRE_REVISION = %d, wire.Revision = %d — these are one "+
			"protocol constant and must change in lockstep (see wire.Revision docs)",
			got, Revision)
	}
}
