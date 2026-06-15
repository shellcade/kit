package gameabi

// Conformance for the large-room-callbacks change: ctx roster-epoch mode
// (sentinel lifecycle: full on first callback / on mutation / on restore;
// unchanged otherwise) and game-declared heartbeat precedence. The loadspike
// guest declares CtxFeatRosterEpoch + HeartbeatMS=100, so it doubles as the
// feature-declaring artifact; the fixture guest declares neither (legacy).

import (
	"context"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// cfgStub is a one-key sdk.ConfigStore for precedence tests.
type cfgStub map[string]string

func (c cfgStub) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := c[key]
	return []byte(v), ok, nil
}

func lsPlayer(i int) sdk.Player {
	ids := []string{"a", "b", "c"}
	return sdk.Player{AccountID: ids[i], Handle: "p" + ids[i], Kind: sdk.KindMember, Conn: "conn-" + ids[i]}
}

func frameHasAt(t *testing.T, tr *sdk.TestRoom, p sdk.Player) {
	t.Helper()
	fr, ok := tr.LastFrame(p)
	if !ok {
		t.Fatalf("no frame for %s", p.Handle)
	}
	for r := 0; r < 22; r++ {
		for c := 0; c < 80; c++ {
			if fr.Cells[r][c].Rune == '@' {
				return
			}
		}
	}
	t.Fatalf("%s's frame has no '@' — guest did not resolve the roster", p.Handle)
}

// The epoch lifecycle: full form on the first callback and on every roster
// mutation; the unchanged form (no member encode) between mutations. Observed
// white-box via the handler's epoch counters plus black-box via the guest
// still rendering members correctly from its cache.
func TestEpochModeLifecycle(t *testing.T) {
	// The loadspike guest generates 12 floors of dungeon in OnStart; under a
	// loaded test machine that can exceed the production 100ms wall-clock
	// deadline, killing the instance and silently no-oping every later
	// callback (epoch stuck at 1). Raise the kill switch like the loadspike
	// benchmark does — this test measures epoch semantics, not latency.
	g, err := LoadGame(loadspikeConsPath, Options{CallbackDeadline: 5 * time.Second})
	if err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 10, MinPlayers: 1, Seed: 7, SeedSet: true}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()})
	wh := h.(*wasmHandler)
	if !wh.epochMode {
		t.Fatal("loadspike declares CtxFeatRosterEpoch; epochMode not set")
	}
	tr := sdk.NewTestRoomFor(h, cfg, sdk.Services{Log: quietLog()})

	tr.Start()
	if wh.rosterEpoch == 0 || wh.lastFullEpoch != wh.rosterEpoch {
		t.Fatalf("start: epoch=%d lastFull=%d — first callback must carry the full form", wh.rosterEpoch, wh.lastFullEpoch)
	}

	tr.Join(lsPlayer(0))
	epochAfterJoin := wh.rosterEpoch
	if epochAfterJoin == 1 || wh.lastFullEpoch != epochAfterJoin {
		t.Fatalf("join: epoch=%d lastFull=%d — mutation must bump and send full", epochAfterJoin, wh.lastFullEpoch)
	}

	// Steady state: ticks must NOT advance the epoch (unchanged form).
	for i := 0; i < 25; i++ {
		tr.Advance(50 * time.Millisecond)
		tr.Tick()
	}
	if wh.rosterEpoch != epochAfterJoin {
		t.Fatalf("steady state bumped the epoch: %d -> %d", epochAfterJoin, wh.rosterEpoch)
	}
	frameHasAt(t, tr, lsPlayer(0)) // guest resolved members from its cache

	// A second join: bump + full again, and BOTH players render.
	tr.Join(lsPlayer(1))
	if wh.rosterEpoch == epochAfterJoin || wh.lastFullEpoch != wh.rosterEpoch {
		t.Fatalf("second join: epoch=%d lastFull=%d", wh.rosterEpoch, wh.lastFullEpoch)
	}
	tr.Advance(50 * time.Millisecond)
	tr.Tick()
	frameHasAt(t, tr, lsPlayer(0))
	frameHasAt(t, tr, lsPlayer(1))

	// Leave: the leave callback's roster still INCLUDES the departed entry
	// (ABI §2), so the epoch bump lands on the NEXT callback, when the
	// roster is actually smaller.
	beforeLeave := wh.rosterEpoch
	tr.Leave(lsPlayer(1))
	tr.Advance(50 * time.Millisecond)
	tr.Tick()
	if wh.rosterEpoch == beforeLeave {
		t.Fatal("post-leave callback did not bump the roster epoch")
	}
	if wh.lastFullEpoch != wh.rosterEpoch {
		t.Fatalf("post-leave full form not sent: epoch=%d lastFull=%d", wh.rosterEpoch, wh.lastFullEpoch)
	}
}

// The legacy guest (fixture: declares no features) keeps legacy encoding —
// epochMode off, counters never engaged beyond the fingerprint bump.
func TestLegacyGuestStaysLegacy(t *testing.T) {
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 2, MinPlayers: 1, Seed: 7, SeedSet: true}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()})
	if h.(*wasmHandler).epochMode {
		t.Fatal("fixture declares no ctx features; epochMode must be off")
	}
	tr := sdk.NewTestRoomFor(h, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(p1)
	tr.Input(p1, runeIn('f')) // personal frame — proves decode still works
	if _, ok := tr.LastFrame(p1); !ok {
		t.Fatal("legacy guest stopped rendering")
	}
}

// Restore: epoch state is ephemeral, so the first post-restore callback
// carries the full form and the guest re-renders from the fresh cache.
func TestEpochModeRestoreSendsFull(t *testing.T) {
	// 5s deadline for the same reason as TestEpochModeLifecycle: don't let a
	// loaded machine kill the heavyweight OnStart.
	g, err := LoadGame(loadspikeConsPath, Options{CallbackDeadline: 5 * time.Second})
	if err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 10, MinPlayers: 1, Seed: 7, SeedSet: true}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()})
	tr := sdk.NewTestRoomFor(h, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(lsPlayer(0))
	tr.Advance(50 * time.Millisecond)
	tr.Tick()

	blob, err := SnapshotHandler(h)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	h2, err := RestoreHandler(g, blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	wh2 := h2.(*wasmHandler)
	if !wh2.epochMode {
		t.Fatal("restored handler lost epochMode")
	}
	if wh2.lastFullEpoch != 0 {
		t.Fatal("restored handler must start with lastFullEpoch=0 (full on first callback)")
	}
	if !wh2.forceFullRoster {
		t.Fatal("restored handler must force the first callback to the full form")
	}
	tr2 := sdk.NewTestRoomFor(h2, cfg, sdk.Services{Log: quietLog()})
	tr2.Join(lsPlayer(0)) // re-seat (same account, same seat)
	if wh2.forceFullRoster {
		t.Fatal("first post-restore callback did not send the full form")
	}
	tr2.Advance(50 * time.Millisecond)
	tr2.Tick()
	frameHasAt(t, tr2, lsPlayer(0))
}

// Heartbeat precedence: admin config > meta declaration > loaded default,
// clamped to the envelope.
func TestMetaHeartbeatPrecedence(t *testing.T) {
	g, err := LoadGame(loadspikeConsPath, Options{}) // meta declares 100ms
	if err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 10, MinPlayers: 1, Seed: 7, SeedSet: true}

	// Declaration wins over the loaded default when no admin config exists.
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()})
	if hb := h.(*wasmHandler).heartbeat; hb != 100*time.Millisecond {
		t.Fatalf("declared heartbeat not applied: %v", hb)
	}

	// Admin config wins over the declaration.
	svc := sdk.Services{Log: quietLog(), Config: cfgStub{"host.heartbeat_ms": "250"}}
	h = g.NewRoom(cfg, svc)
	if hb := h.(*wasmHandler).heartbeat; hb != 250*time.Millisecond {
		t.Fatalf("admin override not applied over declaration: %v", hb)
	}

	// The fixture declares nothing: loaded default stands.
	fg := loadFixture(t, Options{Heartbeat: 50 * time.Millisecond})
	h = fg.NewRoom(cfg, sdk.Services{Log: quietLog()})
	if hb := h.(*wasmHandler).heartbeat; hb != 50*time.Millisecond {
		t.Fatalf("undeclared game's heartbeat: %v", hb)
	}
}
