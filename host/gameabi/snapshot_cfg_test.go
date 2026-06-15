package gameabi

import (
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// TestSnapshotPreservesRoomConfig is the regression guard for the
// hibernation-determinism bug: a restore that dropped Mode/Capacity/MinPlayers
// handed the resumed guest a different CallContext than the control, diverging
// any game that reads those fields (the kit guest decodes the full RoomConfig
// every callback). Snapshot must carry the WHOLE RoomConfig (the resolved seed
// plus mode/capacity/minplayers) so Restore rebuilds the exact context.
func TestSnapshotPreservesRoomConfig(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{
		Mode:       sdk.ModePrivate,
		Capacity:   5,
		MinPlayers: 2,
		Seed:       9991,
		SeedSet:    true,
	}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	r := newReplayRoom([]sdk.Player{p1}, cfg, time.Unix(1_700_000_000, 0))
	h.OnStart(r)
	h.OnJoin(r, p1)

	blob, err := h.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	hB, err := g.Restore(blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if hB.cfg.Mode != cfg.Mode {
		t.Errorf("restored Mode = %q, want %q", hB.cfg.Mode, cfg.Mode)
	}
	if hB.cfg.Capacity != cfg.Capacity {
		t.Errorf("restored Capacity = %d, want %d", hB.cfg.Capacity, cfg.Capacity)
	}
	if hB.cfg.MinPlayers != cfg.MinPlayers {
		t.Errorf("restored MinPlayers = %d, want %d", hB.cfg.MinPlayers, cfg.MinPlayers)
	}
	if hB.cfg.Seed != cfg.Seed {
		t.Errorf("restored Seed = %d, want %d", hB.cfg.Seed, cfg.Seed)
	}
	if !hB.cfg.SeedSet {
		t.Error("restored SeedSet = false, want true")
	}
}

// TestBindServicesRestoresServices guards the second half of the fix: a snapshot
// deliberately does not carry host services, so RestoreHandler returns a handler
// with no services until BindServices rewires them. A restored room without
// services would no-op kv/config/leaderboard host calls and diverge from a live
// room.
func TestBindServicesRestoresServices(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	svc := sdk.Services{Log: quietLog()}
	h := g.NewRoom(cfg, svc).(*wasmHandler)
	r := newReplayRoom([]sdk.Player{p1}, cfg, time.Unix(1_700_000_000, 0))
	h.OnStart(r)

	blob, err := h.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	hB, err := g.Restore(blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// A fresh restore carries no services (Log is nil) — the snapshot is
	// host-resource-free by design.
	if hB.svc.Log != nil {
		t.Error("restored handler unexpectedly carried services from the blob")
	}
	if !BindServices(hB, svc) {
		t.Fatal("BindServices reported false for a wasm handler")
	}
	if hB.svc.Log == nil {
		t.Error("BindServices did not attach the services")
	}
}
