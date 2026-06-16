package conformance_test

import (
	"os"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
)

// The Rust fixture is a SECOND test guest, implemented in Rust from the public
// ABI contract (kit/ABI.md + wire.go) alone — no Go SDK, no shared code with the
// TinyGo fixture. Running it green through the same host adapter and conformance
// harness proves the ABI is language-neutral (add-wasm-game-abi task 5.4).
//
// The artifact is committed (build recipe: `make fixture-rs-wasm`); the t.Skip
// guard only matters for a checkout where it was somehow removed, so CI without
// a Rust toolchain still passes off the committed .wasm.
const fixtureRSPath = "../testdata/fixture-rs/fixture-rs.wasm"

// rustCoreScript exercises the Rust fixture's CORE surface: a two-seat roster
// (joins, a leave, a rejoin), wakes interleaved with clock advances, a
// snapshot/restore checkpoint for the hibernation-determinism check, and an 'e'
// input that ends the room as the final step. Commands the Rust guest does not
// implement fall through to a benign re-render, so the script stays clean.
func rustCoreScript() conformance.Script {
	return conformance.Script{
		conformance.Join(0),
		conformance.Join(1),
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
		conformance.SnapshotRestore(),
		conformance.Advance(50),
		conformance.Wake(),
		conformance.Leave(1),
		conformance.Wake(),
		conformance.Join(1), // rejoin
		conformance.Wake(),
		conformance.Input(0, 'e'), // settle the room (winner seat 0, metric 42)
	}
}

func skipIfNoRustFixture(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fixtureRSPath); err != nil {
		t.Skipf("rust fixture artifact absent (%v); build with `make fixture-rs-wasm`", err)
	}
}

// TestConformanceRustFixturePasses: the Rust guest passes every budget verdict
// and the hibernation-determinism check, proving a non-Go artifact satisfies the
// same ABI through the same host.
func TestConformanceRustFixturePasses(t *testing.T) {
	skipIfNoRustFixture(t)
	rep, err := conformance.Run(fixtureRSPath, gameabi.Options{CallbackDeadline: time.Second}, rustCoreScript())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.ABIVersion != gameabi.Version {
		t.Errorf("abi = %d, want %d", rep.ABIVersion, gameabi.Version)
	}
	if rep.Meta.Slug != "fixture-rs" {
		t.Errorf("meta slug = %q, want fixture-rs", rep.Meta.Slug)
	}
	if rep.Meta.MinPlayers != 1 || rep.Meta.MaxPlayers != 2 {
		t.Errorf("meta players = %d..%d, want 1..2", rep.Meta.MinPlayers, rep.Meta.MaxPlayers)
	}
	if !rep.HibernationChecked || !rep.HibernationOK {
		t.Errorf("hibernation: checked=%v ok=%v, want both true", rep.HibernationChecked, rep.HibernationOK)
	}
	if !rep.Pass() {
		for _, v := range rep.Verdicts {
			if !v.OK {
				t.Errorf("verdict %q failed: limit=%s measured=%s step=%d", v.Name, v.Limit, v.Measured, v.Step)
			}
		}
	}
	if rep.PeakMem == 0 {
		t.Error("peak memory not sampled")
	}
	for _, m := range rep.Steps {
		if m.Faulted {
			t.Errorf("step %d (%s) faulted unexpectedly", m.Index, m.Desc)
		}
	}
}

// TestConformanceRustFixtureTraps: the Rust guest's 'p' command panics
// (panic=abort -> wasm trap), and the host settles the room with a failing
// guest-fault verdict naming the breaching step — the same containment story the
// Go fixture proves, from a different language.
func TestConformanceRustFixtureTraps(t *testing.T) {
	skipIfNoRustFixture(t)
	script := conformance.Script{
		conformance.Join(0),
		conformance.Input(0, 'p'), // deliberate panic
	}
	rep, err := conformance.Run(fixtureRSPath, gameabi.Options{CallbackDeadline: time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Pass() {
		t.Fatal("expected a fault verdict for the panic variant")
	}
	v, ok := findVerdict(rep, "guest fault")
	if !ok {
		t.Fatalf("no guest-fault verdict; verdicts=%+v", rep.Verdicts)
	}
	if v.OK {
		t.Fatal("guest-fault verdict passed despite the panic")
	}
	if v.Step != 1 {
		t.Errorf("fault step = %d, want 1 (the 'p' input)", v.Step)
	}
}
