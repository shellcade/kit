package conformance_test

import (
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
)

// Frame-delta conformance through the REAL host adapter (tasks 6.2–6.4). The
// fixture renders via the v2 SDK (deltas + keyframes); the harness reconstructs
// every frame on the host's per-consumer baseline and compares post-checkpoint
// streams against an uninterrupted control. The byte-identity, solo-rehydrate,
// and Identical-then-Send/mid-join behaviours are exercised end-to-end here;
// the host-side codec round-trip, fuzz, grapheme, and reconciliation invariants
// are unit-tested in internal/gameabi (delta_conformance_test.go).

// TestConformanceDeltaSoloRehydrate is the NAMED solo / same-account rehydrate
// regression (task 6.3 / D6): a solo room is snapshotted mid-script, restored
// into a fresh instance with the SAME account (seat 0) returning, and continued.
// Because the host's baseline cache is ephemeral (re-seeded above the snapshot
// epoch high-water on resume), the restored guest's first post-restore delta is
// epoch-rejected and self-heals to a keyframe; every frame thereafter is
// byte-identical to the uninterrupted control. A divergence beyond the single
// resync frame fails and names the first differing step.
func TestConformanceDeltaSoloRehydrate(t *testing.T) {
	// A solo single-seat script: establish a baseline, snapshot/restore, then
	// continue rendering. The same account (seat 0) is in the room before and
	// after the checkpoint — the hardest case for a guest-infers approach.
	script := conformance.Script{
		conformance.Join(0),
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
		conformance.SnapshotRestore(), // snapshot mid-script; same account returns
		conformance.Advance(50),
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
	}
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.HibernationChecked {
		t.Fatal("hibernation determinism was not checked (no checkpoint ran)")
	}
	if !rep.HibernationOK {
		v, _ := findVerdict(rep, "hibernation determinism")
		t.Fatalf("solo same-account rehydrate diverged: %s", v.Detail)
	}
	if !rep.Pass() {
		for _, v := range rep.Verdicts {
			if !v.OK {
				t.Errorf("verdict %q failed: %s", v.Name, v.Detail)
			}
		}
	}
}

// TestConformanceDeltaIdenticalThenSend drives a broadcast Identical (the
// fixture's default render) interleaved with a per-player Send (the 'f' personal
// frame), so the host's Identical-reconciles-all-slots + later per-player Send
// path runs end-to-end (task 6.4 / D7). The per-player frame after the broadcast
// must reconstruct correctly; the harness asserts the room never faulted and the
// personal frame was delivered.
func TestConformanceDeltaIdenticalThenSend(t *testing.T) {
	script := conformance.Script{
		conformance.Join(0),
		conformance.Join(1),
		conformance.Wake(),        // Identical (broadcast) — reconciles all slots
		conformance.Input(1, 'f'), // per-player Send to seat 1 (PERSONAL frame)
		conformance.Wake(),        // Identical again
		conformance.Input(0, 'f'), // per-player Send to seat 0
	}
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Pass() {
		for _, v := range rep.Verdicts {
			if !v.OK {
				t.Errorf("verdict %q failed: %s", v.Name, v.Detail)
			}
		}
	}
	// The personal-frame inputs must have produced frames without faulting (the
	// host applied the per-player deltas against the reconciled baseline).
	personalSteps := 0
	for _, m := range rep.Steps {
		if m.Callback == "input" {
			if m.Faulted {
				t.Errorf("personal-frame input step %d faulted", m.Index)
			}
			if m.Frames > 0 {
				personalSteps++
			}
		}
	}
	if personalSteps < 2 {
		t.Errorf("personal-frame inputs produced frames in %d steps, want >= 2", personalSteps)
	}
}

// TestConformanceDeltaMidJoin exercises a mid-room join: a second player joins an
// active room (roster indices renumber), so the host bumps the epoch and marks
// affected slots not-present — the joiner's first frame is a keyframe and the
// room renders correctly without faulting (task 6.4 / D7 mid-join scenario).
func TestConformanceDeltaMidJoin(t *testing.T) {
	script := conformance.Script{
		conformance.Join(0),
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
		conformance.Join(1), // mid-room join: indices renumber -> keyframe path
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
	}
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Pass() {
		for _, v := range rep.Verdicts {
			if !v.OK {
				t.Errorf("verdict %q failed: %s", v.Name, v.Detail)
			}
		}
	}
	for _, m := range rep.Steps {
		if m.Faulted {
			t.Errorf("step %d (%s) faulted during the mid-join sequence", m.Index, m.Desc)
		}
	}
}
