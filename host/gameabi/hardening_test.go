package gameabi

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"

	"github.com/shellcade/kit/v2/host/sdk"
	"github.com/shellcade/kit/v2/host/memsvc"
)

// readFixtureWasm reads the committed fixture artifact for module-level
// inspection (import enumeration).
func readFixtureWasm(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", fixturePath, err)
	}
	return b
}

// logCapture is a slog.Handler that records every record's message, so a test
// can assert on what the guest logged through the host `log` function (the host
// routes a guest log line straight to the room log as the record message).
type logCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, r.Message)
	c.mu.Unlock()
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// findLine returns the first captured message carrying substr.
func (c *logCapture) findLine(substr string) (string, bool) {
	for _, m := range c.lines() {
		if strings.Contains(m, substr) {
			return m, true
		}
	}
	return "", false
}

// ---- (a) per-room config-driven knobs ---------------------------------------

// TestConfigKnobsOverrideCadence: an admin's host.heartbeat_ms / host.deadline_ms
// (slug-bound config) override the loaded Options for NEW rooms, clamped to sane
// bounds. The memory cap is NOT config-driven (load-time, manifest-fixed).
func TestConfigKnobsOverrideCadence(t *testing.T) {
	g := loadFixture(t, Options{Heartbeat: 50 * time.Millisecond, CallbackDeadline: 100 * time.Millisecond})

	cases := []struct {
		name           string
		hb, dl         string // config values ("" = unset)
		wantHB, wantDL time.Duration
	}{
		{"unset keeps options", "", "", 50 * time.Millisecond, 100 * time.Millisecond},
		{"valid overrides", "200", "500", 200 * time.Millisecond, 500 * time.Millisecond},
		{"clamp high", "5000", "9000", maxHeartbeat, maxDeadline},
		{"clamp low", "1", "1", minHeartbeat, minDeadline},
		{"malformed ignored", "abc", "-7", 50 * time.Millisecond, 100 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := memsvc.NewFactory(quietLog(), nil)
			if tc.hb != "" {
				f.SetConfig("fixture", cfgHeartbeatMS, []byte(tc.hb))
			}
			if tc.dl != "" {
				f.SetConfig("fixture", cfgDeadlineMS, []byte(tc.dl))
			}
			svc := f.For("room", "fixture")
			cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
			h := g.NewRoom(cfg, svc).(*wasmHandler)
			if h.heartbeat != tc.wantHB {
				t.Errorf("heartbeat = %v, want %v", h.heartbeat, tc.wantHB)
			}
			if h.deadline != tc.wantDL {
				t.Errorf("deadline = %v, want %v", h.deadline, tc.wantDL)
			}
		})
	}
}

// TestConfigKnobsNilStore: a room with no ConfigStore (svc.Config == nil) keeps
// the loaded Options — the override path must tolerate a nil store.
func TestConfigKnobsNilStore(t *testing.T) {
	g := loadFixture(t, Options{Heartbeat: 33 * time.Millisecond, CallbackDeadline: 77 * time.Millisecond})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler) // Config nil
	if h.heartbeat != 33*time.Millisecond || h.deadline != 77*time.Millisecond {
		t.Fatalf("knobs = %v/%v, want 33ms/77ms (nil config keeps options)", h.heartbeat, h.deadline)
	}
}

// TestConfigKnobsDriveSimRate proves the resolved heartbeat actually reaches the
// engine: a room built with host.heartbeat_ms publishes that cadence via
// SetSimRate at OnStart. We drive OnStart against a TestRoom wrapper that
// records the published cadence (the bare TestRoom ignores SetSimRate).
func TestConfigKnobsDriveSimRate(t *testing.T) {
	g := loadFixture(t, Options{Heartbeat: 50 * time.Millisecond})
	f := memsvc.NewFactory(quietLog(), nil)
	f.SetConfig("fixture", cfgHeartbeatMS, []byte("250"))
	svc := f.For("room", "fixture")
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}

	h := g.NewRoom(cfg, svc)
	rec := &simRateRoom{TestRoom: sdk.NewTestRoomFor(h, cfg, svc)}
	h.OnStart(rec)
	if rec.simRate != 250*time.Millisecond {
		t.Fatalf("SetSimRate = %v, want 250ms", rec.simRate)
	}
}

// simRateRoom wraps a TestRoom to capture the SetSimRate cadence the handler
// publishes at OnStart (TestRoom itself ignores SetSimRate).
type simRateRoom struct {
	*sdk.TestRoom
	simRate time.Duration
}

func (r *simRateRoom) SetSimRate(d time.Duration) { r.simRate = d }

// ---- (b) virtualized WASI: time, entropy, network denial --------------------

// fixtureRoomWithLog builds a TestRoom over the fixture with a capturing log,
// returning the room and the capture so a test can drive commands and read the
// guest's log lines.
func fixtureRoomWithLog(t *testing.T, opts Options, seed int64) (*sdk.TestRoom, *logCapture) {
	t.Helper()
	g := loadFixture(t, opts)
	cap := &logCapture{}
	svc := sdk.Services{Log: slog.New(cap)}
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: seed, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, svc)
	tr.Start()
	tr.Join(p1)
	return tr, cap
}

// TestGuestTimeIsRoomClock: the guest's own time.Now() ('t') reads the room
// clock (== CallContext time), proving the host's WithWalltime/WithNanotime
// virtualization. Advancing the room clock advances the guest's clock in step.
func TestGuestTimeIsRoomClock(t *testing.T) {
	tr, cap := fixtureRoomWithLog(t, Options{}, 7)

	tr.Input(p1, runeIn('t'))
	line, ok := cap.findLine("fixture: now=")
	if !ok {
		t.Fatalf("no time log; got %v", cap.lines())
	}
	got := strings.TrimPrefix(line, "fixture: now=")
	want := strconv.FormatInt(tr.Clock.UnixNano(), 10)
	if got != want {
		t.Fatalf("guest now=%s, want room clock %s", got, want)
	}

	// Advance the room clock; the guest sees the new instant on the next call.
	tr.Advance(1234 * time.Millisecond)
	tr.Input(p1, runeIn('t'))
	lines := cap.lines()
	last := lines[len(lines)-1]
	got2 := strings.TrimPrefix(last, "fixture: now=")
	want2 := strconv.FormatInt(tr.Clock.UnixNano(), 10)
	if got2 != want2 {
		t.Fatalf("after advance guest now=%s, want %s", got2, want2)
	}
}

// TestEntropyIsSeeded: two rooms with the SAME seed log identical 'r' entropy
// (the host's WithRandSource is room-seeded), and two rooms with DIFFERENT
// seeds diverge. This proves entropy is host-virtualized and reproducible —
// the guest never reaches the system CSPRNG.
func TestEntropyIsSeeded(t *testing.T) {
	read := func(seed int64) string {
		tr, cap := fixtureRoomWithLog(t, Options{}, seed)
		tr.Input(p1, runeIn('r'))
		line, ok := cap.findLine("fixture: rand=")
		if !ok {
			t.Fatalf("seed %d: no entropy log; got %v", seed, cap.lines())
		}
		return strings.TrimPrefix(line, "fixture: rand=")
	}
	a1, a2 := read(7), read(7)
	if a1 != a2 {
		t.Fatalf("same seed produced different entropy: %s vs %s", a1, a2)
	}
	b := read(99)
	if a1 == b {
		t.Fatalf("different seeds produced identical entropy: %s", a1)
	}
}

// TestNoNetworkCapability proves the artifact cannot reach the network: the
// only modules it imports are the shellcade host namespace, the extism kernel
// env, and non-socket WASI. We compile the wasm with a vanilla wazero runtime
// (no host modules registered, so an empty extism AllowedHosts is mirrored) and
// enumerate ImportedFunctions — asserting NO socket/network import is present.
// What this proves: even if the guest tried to dial out, there is no host
// function bound for it to call; the import simply does not exist in the module.
func TestNoNetworkCapability(t *testing.T) {
	wasm := readFixtureWasm(t)
	rt := wazero.NewRuntime(context.Background())
	defer rt.Close(context.Background())
	compiled, err := rt.CompileModule(context.Background(), wasm)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer compiled.Close(context.Background())

	// WASI socket primitives (wasi_snapshot_preview1) and any plausible network
	// host import. None must appear in the artifact's import list.
	banned := map[string]bool{
		"sock_accept": true, "sock_recv": true, "sock_send": true,
		"sock_shutdown": true, "sock_open": true, "sock_connect": true,
		"sock_bind": true, "sock_listen": true, "sock_setsockopt": true,
		"sock_getsockopt": true, "sock_addr_resolve": true,
	}
	bannedModules := map[string]bool{
		"wasi_snapshot_preview1_net": true,
		"wasi:sockets":               true,
		"http":                       true,
	}

	for _, fn := range compiled.ImportedFunctions() {
		mod, name, _ := fn.Import()
		if bannedModules[mod] {
			t.Fatalf("artifact imports network module %q.%q", mod, name)
		}
		if banned[name] {
			t.Fatalf("artifact imports network capability %q.%q", mod, name)
		}
	}
	// Sanity: the artifact DOES import the shellcade host namespace (so the
	// enumeration is meaningful, not just empty).
	sawHost := false
	for _, fn := range compiled.ImportedFunctions() {
		if mod, _, _ := fn.Import(); strings.Contains(mod, "extism:host/user") {
			sawHost = true
			break
		}
	}
	if !sawHost {
		t.Fatal("artifact imports no shellcade host module — import enumeration is suspect")
	}
}

// ---- (c) scripted countdown (deadline driven by wake + CallContext time) ----

// TestScriptedCountdown drives the 'd' command through TestRoom: arm a 250ms
// countdown, then Advance the clock across Ticks. Each wake renders the
// remaining ms (read from CallContext time), and the wake that finds the clock
// past the deadline renders BOOM and ends the room — no host-side timer, purely
// wake + virtualized clock.
func TestScriptedCountdown(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(p1)

	tr.Input(p1, runeIn('d')) // arm; renders the first remaining-ms frame
	f, _ := tr.LastFrame(p1)
	if got := rowText(f, 0); got != "COUNTDOWN" {
		t.Fatalf("after arm row0 = %q, want COUNTDOWN", got)
	}
	if got := rowText(f, 1); got != "remaining_ms=250" {
		t.Fatalf("after arm row1 = %q, want remaining_ms=250", got)
	}

	// Three 100ms steps: 250 -> 150 -> 50 -> BOOM.
	wantRemaining := []string{"remaining_ms=150", "remaining_ms=50"}
	for i, want := range wantRemaining {
		tr.Advance(100 * time.Millisecond)
		tr.Tick()
		if tr.Ended {
			t.Fatalf("step %d: room ended early", i)
		}
		f, _ := tr.LastFrame(p1)
		if got := rowText(f, 1); got != want {
			t.Fatalf("step %d row1 = %q, want %q", i, got, want)
		}
	}

	tr.Advance(100 * time.Millisecond) // clock now past the deadline
	tr.Tick()
	f, _ = tr.LastFrame(p1)
	if got := rowText(f, 0); got != "BOOM" {
		t.Fatalf("final row0 = %q, want BOOM", got)
	}
	if !tr.Ended {
		t.Fatal("room did not end after the deadline passed")
	}
}
