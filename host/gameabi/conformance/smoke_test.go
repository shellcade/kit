package conformance_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
)

func smokeRun(t *testing.T, script conformance.Script, seats int) []conformance.SeatFrames {
	t.Helper()
	shots, err := conformance.RunShots(fixturePath, gameabi.Options{}, conformance.SmokeRun{
		Seed:   42,
		Seats:  seats,
		Epoch:  time.Date(2000, 1, 1, 0, 0, 42, 0, time.UTC),
		Script: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	return shots
}

func TestRunShotsCapturesBroadcast(t *testing.T) {
	shots := smokeRun(t, conformance.Script{
		conformance.Shot("start", nil),
		conformance.Wake(),
		conformance.Shot("after-wake", []int{1, 0}),
	}, 2)
	if len(shots) != 2 {
		t.Fatalf("shots: %d", len(shots))
	}
	s := shots[0]
	if s.Name != "start" || len(s.Frames) != 2 {
		t.Fatalf("shot 0: %q frames=%d", s.Name, len(s.Frames))
	}
	// The fixture broadcasts identically — both seats' grids match.
	if s.Frames[0] != s.Frames[1] {
		t.Fatal("broadcast frames should be identical")
	}
}

func TestRunShotsPersonalFrameDiffers(t *testing.T) {
	// 'f' sends a personal frame to the inputting player only.
	shots := smokeRun(t, conformance.Script{
		conformance.Input(0, 'f'),
		conformance.Shot("personal", nil),
	}, 2)
	s := shots[0]
	if s.Frames[0] == s.Frames[1] {
		t.Fatal("seat 0 received a personal frame; seats must differ")
	}
}

func TestRunShotsAdvanceMovesGuestClock(t *testing.T) {
	// 't' renders the guest's own clock reading; two shots around an advance
	// must differ (the guest sees the new CallContext time).
	a := smokeRun(t, conformance.Script{
		conformance.Input(0, 't'),
		conformance.Shot("before", nil),
	}, 1)
	b := smokeRun(t, conformance.Script{
		conformance.Advance(500), conformance.Wake(),
		conformance.Input(0, 't'),
		conformance.Shot("after", nil),
	}, 1)
	if a[0].Frames[0] == b[0].Frames[0] {
		t.Fatal("advancing the clock must be visible to the guest")
	}
}

func TestRunShotsDeterministic(t *testing.T) {
	script := conformance.Script{
		conformance.Input(0, 'f'),
		conformance.Advance(50), conformance.Wake(),
		conformance.Shot("end", nil),
	}
	a := smokeRun(t, script, 2)
	b := smokeRun(t, script, 2)
	if len(a) != len(b) {
		t.Fatal("shot count differs")
	}
	for i := range a {
		for j := range a[i].Frames {
			if a[i].Frames[j] != b[i].Frames[j] {
				t.Fatalf("shot %d frame %d differs across identical runs", i, j)
			}
		}
	}
}

func TestRunShotsErrors(t *testing.T) {
	if _, err := conformance.RunShots(fixturePath, gameabi.Options{}, conformance.SmokeRun{
		Seed: 1, Seats: 99, Epoch: time.Unix(0, 0), Script: conformance.Script{conformance.Shot("a", nil)},
	}); err == nil || !strings.Contains(err.Error(), "seats") {
		t.Fatalf("want seats error, got %v", err)
	}
	if _, err := conformance.RunShots(fixturePath, gameabi.Options{}, conformance.SmokeRun{
		Seed: 1, Seats: 1, Epoch: time.Unix(0, 0), Script: conformance.Script{conformance.Join(0)},
	}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("want step-kind error, got %v", err)
	}
}

func TestRunIgnoresShotSteps(t *testing.T) {
	// The existing Run treats Shot markers as no-ops — a smoke script must not
	// perturb conformance metrics.
	rep, err := conformance.Run(fixturePath, gameabi.Options{}, conformance.Script{
		conformance.Join(0),
		conformance.Shot("ignored", nil),
		conformance.Wake(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass() {
		t.Fatal("script with a shot marker should still pass")
	}
}
