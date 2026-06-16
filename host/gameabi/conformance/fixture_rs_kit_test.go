package conformance_test

import (
	"os"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
)

// fixture-rs-kit is the conformance fixture built ON the shellcade-kit Rust SDK
// crate (kit/rust). Where fixture-rs proves the ABI is implementable from
// ABI.md alone (and stays SDK-free for exactly that reason), THIS fixture
// proves the SDK passes the server's own gate: its delta path (per-slot
// baselines, host-authoritative epochs, keyframe bootstrap, in-call retry on
// epoch rejection) runs under the same script — including the snapshot/restore
// hibernation byte-identity check, which rejects the post-restore delta and
// forces the SDK's keyframe-retry path to actually fire.
//
// The artifact is committed (build recipe: `make fixture-rs-kit-wasm`); the
// t.Skip guard only matters for a checkout where it was removed, so CI without
// a Rust toolchain still passes off the committed .wasm.
const fixtureRSKitPath = "../testdata/fixture-rs-kit/fixture-rs-kit.wasm"

func skipIfNoRustKitFixture(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fixtureRSKitPath); err != nil {
		t.Skipf("rust kit fixture artifact absent (%v); build with `make fixture-rs-kit-wasm`", err)
	}
}

// TestConformanceRustKitFixturePasses: the SDK-built guest passes every budget
// verdict and the hibernation-determinism check. The script mirrors the
// fixture-rs core script (joins, leave, rejoin, wakes with clock advances, a
// snapshot/restore checkpoint, settle via 'e').
func TestConformanceRustKitFixturePasses(t *testing.T) {
	skipIfNoRustKitFixture(t)
	script := conformance.Script{
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
		conformance.Input(0, 'e'), // settle (winner seat 0, metric 42)
	}
	rep, err := conformance.Run(fixtureRSKitPath, gameabi.Options{CallbackDeadline: time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.ABIVersion != gameabi.Version {
		t.Errorf("abi = %d, want %d", rep.ABIVersion, gameabi.Version)
	}
	if rep.Meta.Slug != "fixture-rs-kit" {
		t.Errorf("meta slug = %q, want fixture-rs-kit", rep.Meta.Slug)
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

// TestConformanceRustKitFixtureTraps: the SDK guest's 'p' command panics
// (panic=abort -> wasm trap) and the host contains it — the same fault story
// as the hand-rolled fixtures, through the SDK's dispatch.
func TestConformanceRustKitFixtureTraps(t *testing.T) {
	skipIfNoRustKitFixture(t)
	script := conformance.Script{
		conformance.Join(0),
		conformance.Input(0, 'p'), // deliberate panic
	}
	rep, err := conformance.Run(fixtureRSKitPath, gameabi.Options{CallbackDeadline: time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Pass() {
		t.Fatal("report passed; want a failing guest-fault verdict")
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
