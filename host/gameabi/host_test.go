package gameabi

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// The committed fixture guest (testdata/fixture/): renders a status frame and
// misbehaves on command — 'p' panic, 'l' spin, 'o' allocate, 'e' end.
const fixturePath = "testdata/fixture/fixture.wasm"

var (
	p1 = sdk.Player{AccountID: "a1", Handle: "ada", Kind: sdk.KindMember, Conn: "c1"}
	p2 = sdk.Player{AccountID: "a2", Handle: "bob", Kind: sdk.KindMember, Conn: "c2"}
)

func loadFixture(t *testing.T, opts Options) sdk.Game {
	t.Helper()
	g, err := LoadGame(fixturePath, opts)
	if err != nil {
		t.Fatalf("LoadGame(%s): %v", fixturePath, err)
	}
	return g
}

func runeIn(r rune) sdk.Input { return sdk.Input{Kind: sdk.InputRune, Rune: r} }

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestFixtureHandshake(t *testing.T) {
	g := loadFixture(t, Options{})
	m := g.Meta()
	if m.Slug != "fixture" || m.MaxPlayers != 2 {
		t.Fatalf("meta = %+v, want slug=fixture max=2", m)
	}
}

// TestLoadGameBytes proves the in-memory loader (the catalog loads a verified
// blob from the object store, never a path) yields an equivalent game to the
// file-backed LoadGame.
func TestLoadGameBytes(t *testing.T) {
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	g, err := LoadGameBytes(b, Options{})
	if err != nil {
		t.Fatalf("LoadGameBytes: %v", err)
	}
	if g.Meta().Slug != "fixture" {
		t.Fatalf("LoadGameBytes meta slug = %q, want fixture", g.Meta().Slug)
	}
	// The host-composed namespaced slug applies to a bytes-loaded game too.
	if !OverrideSlug(g, "alice/fixture") || g.Meta().Slug != "alice/fixture" {
		t.Fatalf("OverrideSlug on a bytes-loaded game failed: %q", g.Meta().Slug)
	}
}

// TestDerivedJoinability proves the host-owned phase: open ⇔ unsettled ∧
// below capacity, published after every callback (the matchmaker's Quick and
// PrivateJoin read exactly this).
func TestDerivedJoinability(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 2, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})

	assertOpen := func(step string, want bool) {
		t.Helper()
		ph, ok := tr.LastPhase()
		if !ok {
			t.Fatalf("%s: no phase published", step)
		}
		if ph.Open != want || ph.Name != "in play" {
			t.Fatalf("%s: phase = %+v, want open=%v name=%q", step, ph, want, "in play")
		}
	}

	tr.Start()
	assertOpen("start (0/2)", true)
	tr.Join(p1)
	assertOpen("join p1 (1/2)", true)
	tr.Join(p2)
	assertOpen("join p2 (2/2 full)", false)
	tr.Leave(p2)
	assertOpen("leave p2 (1/2)", true)

	// Guest ends the room: no phase is published after end (the engine's
	// settled phase must win), and the result carries the guest's ranking.
	before := len(tr.Phases)
	tr.Input(p1, runeIn('e'))
	if !tr.Ended {
		t.Fatal("input 'e': room did not end")
	}
	if len(tr.Phases) != before {
		t.Fatalf("phase published after guest end: %+v", tr.Phases[before:])
	}
	res, ok := tr.Result()
	if !ok {
		t.Fatal("no result after guest end")
	}
	found := false
	for _, pr := range res.Rankings {
		if pr.Player == p1 {
			found = pr.Metric == 42 && pr.Rank == 1 && pr.Status == sdk.StatusFinished
		}
	}
	if !found {
		t.Fatalf("result = %+v, want p1 finished metric 42 rank 1", res)
	}
}

// TestDerivedJoinabilityUnlimited: capacity 0 means no cap — always open while
// unsettled.
func TestDerivedJoinabilityUnlimited(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModePrivate, Capacity: 0, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(p1)
	tr.Join(p2)
	ph, ok := tr.LastPhase()
	if !ok || !ph.Open {
		t.Fatalf("phase = %+v ok=%v, want open with no capacity", ph, ok)
	}
}

// TestRendersFrames: the fixture broadcasts a frame on join.
func TestRendersFrames(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(p1)
	f, ok := tr.LastFrame(p1)
	if !ok {
		t.Fatal("no frame after join")
	}
	if got := f.Cells[0][0].Rune; got != 'F' {
		t.Fatalf("frame cell(0,0) = %q, want 'F' (FIXTURE banner)", got)
	}
}

// ---- live-runtime tests (the real actor goroutine the matchmaker drives) ----

func newLiveRoom(t *testing.T, g sdk.Game, cfg sdk.RoomConfig) sdk.RoomCtl {
	t.Helper()
	svc := sdk.Services{Log: quietLog()}
	ctl := sdk.NewRoomRuntime("test-"+t.Name(), g.NewRoom(cfg, svc), cfg, svc)
	t.Cleanup(func() { _ = ctl.Close() })
	return ctl
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestLiveJoinability is the quick-match regression: the matchmaker prunes
// rooms whose Snapshot is not Open, so a live wasm room must publish open
// while unsettled and below capacity, closed when full.
func TestLiveJoinability(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 2, MinPlayers: 1, Seed: 7, SeedSet: true}
	ctl := newLiveRoom(t, g, cfg)

	if err := ctl.Join(p1); err != nil {
		t.Fatalf("join p1: %v", err)
	}
	waitFor(t, "open after first join", func() bool { return ctl.Snapshot().Open })

	if err := ctl.Join(p2); err != nil {
		t.Fatalf("join p2: %v", err)
	}
	waitFor(t, "closed when full", func() bool {
		ph := ctl.Snapshot()
		return !ph.Open && !ph.Settled
	})

	ctl.Leave(p2)
	waitFor(t, "reopen after leave", func() bool { return ctl.Snapshot().Open })
}

// TestContainment proves a faulting guest settles ONLY its own room: a panic
// ('p'), a spin past the callback deadline ('l'), and allocation past the
// memory cap ('o') each kill room A while room B — same compiled game — keeps
// answering.
func TestContainment(t *testing.T) {
	cases := []struct {
		name string
		cmd  rune
		opts Options
	}{
		{"trap", 'p', Options{}},
		{"deadline", 'l', Options{CallbackDeadline: 50 * time.Millisecond}},
		{"oom", 'o', Options{MemoryPages: 80, CallbackDeadline: 5 * time.Second}}, // 5 MiB cap
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := loadFixture(t, tc.opts)
			cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
			ctlA := newLiveRoom(t, g, cfg)
			ctlB := newLiveRoom(t, g, cfg)
			if err := ctlA.Join(p1); err != nil {
				t.Fatalf("join A: %v", err)
			}
			if err := ctlB.Join(p2); err != nil {
				t.Fatalf("join B: %v", err)
			}
			framesB := ctlB.Frames(p2)

			ctlA.Input(p1, runeIn(tc.cmd))
			select {
			case <-ctlA.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("room A did not settle after fault")
			}
			if ph := ctlA.Snapshot(); !ph.Settled {
				t.Fatalf("room A phase = %+v, want settled", ph)
			}

			// Room B still answers: an input produces a fresh frame.
			drain(framesB)
			ctlB.Input(p2, runeIn('x'))
			select {
			case _, ok := <-framesB:
				if !ok {
					t.Fatal("room B frame stream closed — fault leaked across rooms")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("room B unresponsive after room A fault")
			}
			if ph := ctlB.Snapshot(); ph.Settled {
				t.Fatal("room B settled — fault leaked across rooms")
			}
		})
	}
}

func drain(ch <-chan sdk.Frame) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// TestRoomsAreIsolatedInstances: two rooms of one compiled game hold distinct
// guest memories (wake counters advance independently).
func TestRoomsAreIsolatedInstances(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	trA := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	trB := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	trA.Start()
	trB.Start()
	trA.Join(p1)
	trB.Join(p2)
	trA.Tick() // one wake in A only
	fA, _ := trA.LastFrame(p1)
	fB, _ := trB.LastFrame(p2)
	rowA, rowB := rowText(fA, 2), rowText(fB, 2)
	if rowA != "wakes=1" || rowB != "wakes=0" {
		t.Fatalf("wake rows = %q / %q, want wakes=1 / wakes=0", rowA, rowB)
	}
}

func rowText(f sdk.Frame, row int) string {
	var out []rune
	for col := 0; col < 80; col++ {
		r := f.Cells[row][col].Rune
		if r == 0 || r == ' ' {
			break
		}
		out = append(out, r)
	}
	return string(out)
}
