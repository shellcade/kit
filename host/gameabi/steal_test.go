package gameabi

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// stealRecordingMetrics is the recording Metrics double that ALSO implements the
// optional StealMetrics extension — proving the kill site's type assertion finds
// and drives the steal record only when the implementer opts in.
type stealRecordingMetrics struct {
	*recordingMetrics
	mu          sync.Mutex
	stealKills  map[string]int // slug/callback -> count
	stealKillsN int
}

func newStealRecordingMetrics() *stealRecordingMetrics {
	return &stealRecordingMetrics{recordingMetrics: newRecordingMetrics(), stealKills: map[string]int{}}
}

func (m *stealRecordingMetrics) GameCallbackStealDeadline(slug, callback string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stealKills[slug+"/"+callback]++
	m.stealKillsN++
}

func (m *stealRecordingMetrics) totalStealKills() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stealKillsN
}

// stubStealReader installs an injectable steal reader for the duration of a test
// and restores the previous one. delta is added to the cumulative counter on
// every read, so delta==0 models a quiet host (no steal advances across any
// callback) and delta>0 models a VM under steal (every callback's window
// advances). ok==false models a host without the signal (non-Linux / read fail).
func stubStealReader(t *testing.T, delta uint64, ok bool) {
	t.Helper()
	prev := stealReader
	var ctr atomic.Uint64
	stealReader = func() (uint64, bool) {
		return ctr.Add(delta), ok
	}
	t.Cleanup(func() { stealReader = prev })
}

// TestStealDeadlineDetection proves the DETECTION-ONLY steal seam: at a real
// wall-clock callback-deadline kill (a non-exempt fixture spinning on 'l'), the
// steal record is emitted ALONGSIDE GameCallbackDeadline iff host-stolen CPU
// advanced across the killed callback's window — and never on the host-I/O path.
// The kill/fault behavior itself is unchanged in every case.
func TestStealDeadlineDetection(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stealDelta     uint64 // per-read advance of the stubbed steal counter
		stealOK        bool   // whether the stub reports the signal as available
		wantDeadline   int    // GameCallbackDeadline records expected
		wantStealKills int    // GameCallbackStealDeadline records expected
	}{
		{name: "steal unchanged => deadline only, no steal metric", stealDelta: 0, stealOK: true, wantDeadline: 1, wantStealKills: 0},
		{name: "steal advanced => both deadline and steal metric", stealDelta: 5, stealOK: true, wantDeadline: 1, wantStealKills: 1},
		{name: "no steal signal => deadline only (current behavior)", stealDelta: 9, stealOK: false, wantDeadline: 1, wantStealKills: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubStealReader(t, tc.stealDelta, tc.stealOK)

			var faults atomic.Int32
			rec := newStealRecordingMetrics()
			// NON-EXEMPT game: OnFault != nil, so it can actually fault/quarantine
			// (the load-test game is QuarantineExempt and must never be used here).
			g := loadFixture(t, Options{
				CallbackDeadline: 50 * time.Millisecond,
				OnFault:          func(string) { faults.Add(1) },
				Metrics:          rec,
			})
			cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
			tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
			tr.Start()
			tr.Join(p1)

			tr.Input(p1, runeIn('l')) // spin forever -> real wall-clock deadline kill

			if !tr.Ended {
				t.Fatal("room did not settle after a spin-to-deadline")
			}
			// Kill/fault behavior is unchanged regardless of steal: still exactly
			// one fault feeding quarantine.
			if n := faults.Load(); n != 1 {
				t.Fatalf("faults = %d, want 1 (kill behavior must be unchanged)", n)
			}
			if got := rec.totalDeadlines(); got != tc.wantDeadline {
				t.Fatalf("GameCallbackDeadline = %d, want %d", got, tc.wantDeadline)
			}
			if got := rec.totalStealKills(); got != tc.wantStealKills {
				t.Fatalf("GameCallbackStealDeadline = %d, want %d", got, tc.wantStealKills)
			}
			// The steal seam never reclassifies a kill as host-I/O.
			if got := rec.totalHostIODeadlines(); got != 0 {
				t.Fatalf("host-I/O deadline = %d, want 0 (the guest spun)", got)
			}
		})
	}
}

// TestStealDeadlineNotRecordedOnHostIOKill proves the steal seam stays silent on
// the host-I/O kill path: a deadline that expired inside the host's OWN kv call
// (slow Postgres) records neither GameCallbackDeadline nor the steal metric,
// even with steal advancing — that path is already exempt and unchanged.
func TestStealDeadlineNotRecordedOnHostIOKill(t *testing.T) {
	stubStealReader(t, 7, true) // steal advancing on every callback

	var faults atomic.Int32
	rec := newStealRecordingMetrics()
	g := loadFixture(t, Options{
		CallbackDeadline: 50 * time.Millisecond,
		OnFault:          func(string) { faults.Add(1) },
		Metrics:          rec,
	})
	// A kv store slower than the deadline, with the guest blocked in the host's
	// own kv call: the host-I/O carve-out fires (hostIOKill), not a fault.
	svc := sdk.Services{Log: quietLog(), Accounts: fakeAccounts{kv: slowKV{d: 30 * time.Second}}}
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, svc)
	tr.Start()
	tr.Join(p1)

	tr.Input(p1, runeIn('k')) // fixture's kv command: blocks in the host kv_set call

	if !tr.Ended {
		t.Fatal("room did not settle after the host-I/O deadline")
	}
	if n := faults.Load(); n != 0 {
		t.Fatalf("faults = %d, want 0 (host-I/O kill must not fault)", n)
	}
	if got := rec.totalHostIODeadlines(); got != 1 {
		t.Fatalf("host-I/O deadline = %d, want 1", got)
	}
	if got := rec.totalDeadlines(); got != 0 {
		t.Fatalf("GameCallbackDeadline = %d, want 0 on the host-I/O path", got)
	}
	if got := rec.totalStealKills(); got != 0 {
		t.Fatalf("GameCallbackStealDeadline = %d, want 0 on the host-I/O path", got)
	}
}
