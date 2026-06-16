package conformance_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
)

const fixturePath = "../testdata/fixture/fixture.wasm"

// defaultScript exercises every export against the fixture: a two-seat roster
// sequence (joins, interleaved inputs, leave, rejoin), all the benign ABI
// commands, and a snapshot/restore checkpoint for the hibernation determinism
// check.
func defaultScript() conformance.Script {
	return conformance.Script{
		conformance.Join(0),
		conformance.Join(1),
		conformance.Input(0, 'i'), // set input context
		conformance.Input(1, 'c'), // config_get
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
		conformance.Input(0, 'r'), // entropy draw
		conformance.Input(1, 'f'), // personal frame
		conformance.SnapshotRestore(),
		conformance.Input(0, 't'), // time
		conformance.Advance(50),
		conformance.Wake(),
		conformance.Leave(1),
		conformance.Wake(),
		conformance.Join(1), // rejoin
		conformance.Wake(),
	}
}

// TestConformanceFixturePasses: the well-behaved fixture passes every budget
// verdict and the hibernation-determinism check.
func TestConformanceFixturePasses(t *testing.T) {
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: time.Second}, defaultScript())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.ABIVersion != gameabi.Version {
		t.Errorf("abi = %d, want %d", rep.ABIVersion, gameabi.Version)
	}
	if rep.Meta.Slug != "fixture" {
		t.Errorf("meta slug = %q, want fixture", rep.Meta.Slug)
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
	// Every benign callback ran clean.
	for _, m := range rep.Steps {
		if m.Faulted {
			t.Errorf("step %d (%s) faulted unexpectedly", m.Index, m.Desc)
		}
	}
}

// TestConformanceTwoSeatRoster: the report has roster-driving callbacks for both
// seats (join, input, leave, rejoin) and the room never faulted.
func TestConformanceTwoSeatRoster(t *testing.T) {
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: time.Second}, defaultScript())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	joins, leaves, inputs := 0, 0, 0
	for _, m := range rep.Steps {
		switch m.Callback {
		case "join":
			joins++
		case "leave":
			leaves++
		case "input":
			inputs++
		}
	}
	if joins != 3 { // seat0, seat1, seat1-rejoin
		t.Errorf("join callbacks = %d, want 3", joins)
	}
	if leaves != 1 {
		t.Errorf("leave callbacks = %d, want 1", leaves)
	}
	if inputs < 4 {
		t.Errorf("input callbacks = %d, want >= 4", inputs)
	}
}

// TestConformanceDeadlineFails: the 'l' spin variant breaches the per-callback
// deadline; the failing verdict names the limit, the measured latency, and the
// step.
func TestConformanceDeadlineFails(t *testing.T) {
	script := conformance.Script{
		conformance.Join(0),
		conformance.Input(0, 'l'), // spin past the deadline
	}
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: 50 * time.Millisecond}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Pass() {
		t.Fatal("expected a budget breach for the spin variant")
	}
	v, ok := findVerdict(rep, "callback deadline")
	if !ok {
		// The latency verdict also names the deadline; accept either.
		v, ok = findFailing(rep)
	}
	if !ok {
		t.Fatalf("no failing verdict; verdicts=%+v", rep.Verdicts)
	}
	if v.Step < 0 {
		t.Errorf("breach verdict %q did not name a step", v.Name)
	}
	if v.Limit == "" || v.Measured == "" {
		t.Errorf("breach verdict %q must name limit (%q) and measured (%q)", v.Name, v.Limit, v.Measured)
	}
	// The breaching step must be the spin input (index 1).
	if v.Step != 1 {
		t.Errorf("breach step = %d, want 1 (the 'l' input)", v.Step)
	}
	if !strings.Contains(v.Limit, "ms") {
		t.Errorf("deadline limit %q should be a duration", v.Limit)
	}
}

// TestConformanceTrapFails: the 'p' panic variant traps; the failing verdict
// names the fault and the step.
func TestConformanceTrapFails(t *testing.T) {
	script := conformance.Script{
		conformance.Join(0),
		conformance.Input(0, 'p'), // deliberate panic
	}
	rep, err := conformance.Run(fixturePath, gameabi.Options{CallbackDeadline: time.Second}, script)
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
	if v.Measured == "" {
		t.Errorf("fault verdict must name the measured value (exit/trap)")
	}
}

// TestConformanceMemoryNamed: a tiny memory cap + the 'o' allocate-forever
// command breaches the linear-memory budget, and the verdict names the cap and
// the measured peak.
func TestConformanceMemoryNamed(t *testing.T) {
	script := conformance.Script{
		conformance.Join(0),
		conformance.Input(0, 'o'), // allocate past the cap
	}
	// 5 MiB cap (80 pages); allocate-forever trips the cap (the guest traps).
	rep, err := conformance.Run(fixturePath, gameabi.Options{MemoryPages: 80, CallbackDeadline: 5 * time.Second}, script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Pass() {
		t.Fatal("expected a breach for allocate-forever")
	}
	mv, ok := findVerdict(rep, "linear memory")
	if !ok {
		t.Fatalf("no linear-memory verdict; verdicts=%+v", rep.Verdicts)
	}
	if mv.Limit == "" {
		t.Errorf("memory verdict must name the cap")
	}
	// The allocate-forever guest traps when it hits the cap, so the guest-fault
	// verdict must also fail and name the step.
	fv, _ := findVerdict(rep, "guest fault")
	if fv.OK {
		t.Errorf("guest-fault verdict passed despite hitting the memory cap")
	}
}

func findVerdict(r conformance.Report, name string) (conformance.Verdict, bool) {
	for _, v := range r.Verdicts {
		if v.Name == name {
			return v, true
		}
	}
	return conformance.Verdict{}, false
}

func findFailing(r conformance.Report) (conformance.Verdict, bool) {
	for _, v := range r.Verdicts {
		if !v.OK {
			return v, true
		}
	}
	return conformance.Verdict{}, false
}
