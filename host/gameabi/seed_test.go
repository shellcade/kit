package gameabi

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/host/sdk"
)

// readGameRand starts a fixture room with cfg, presses 'g', and returns the
// guest's logged r.Rand() value — the SDK room PRNG the guest runtime seeds
// from the CallContext seed (NOT the 'r' WASI entropy source).
func readGameRand(t *testing.T, cfg sdk.RoomConfig) string {
	t.Helper()
	g := loadFixture(t, Options{})
	cap := &logCapture{}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: slog.New(cap)})
	tr.Start()
	tr.Join(p1)
	tr.Input(p1, runeIn('g'))
	line, ok := cap.findLine("fixture: game_rand=")
	if !ok {
		t.Fatalf("no game_rand log; got %v", cap.lines())
	}
	return strings.TrimPrefix(line, "fixture: game_rand=")
}

// TestUnseededRoomsGetDistinctGameRand: two production-shaped rooms (no
// explicit seed: Seed=0, SeedSet=false) must NOT share an r.Rand() stream.
// The guest runtime seeds its PRNG from the CallContext seed verbatim, so the
// host must place a per-room derived seed there when the operator did not set
// one — otherwise every room of every game deals the identical "random"
// sequence (and casino outcomes are learnable by replaying a fresh room).
func TestUnseededRoomsGetDistinctGameRand(t *testing.T) {
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1}
	a, b := readGameRand(t, cfg), readGameRand(t, cfg)
	if a == b {
		t.Fatalf("two unseeded rooms produced the identical r.Rand() stream: %s", a)
	}
}

// TestExplicitSeedKeepsGameRandDeterministic: the dev/--seed contract is
// untouched by the unseeded-room derivation — the same explicit seed still
// reproduces the same r.Rand() stream.
func TestExplicitSeedKeepsGameRandDeterministic(t *testing.T) {
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	if s1, s2 := readGameRand(t, cfg), readGameRand(t, cfg); s1 != s2 {
		t.Fatalf("same explicit seed produced different r.Rand() streams: %s vs %s", s1, s2)
	}
	other := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 8, SeedSet: true}
	if s1, s3 := readGameRand(t, cfg), readGameRand(t, other); s1 == s3 {
		t.Fatalf("different explicit seeds produced the identical r.Rand() stream: %s", s1)
	}
}
