// Pokies — the shellcade devkit reference game: a wake-driven port of the
// native pokies cabinet to the wasm ABI (gamekit). Demonstrates the canonical
// idioms: a reel animation derived from CallContext time, one-shot deadlines
// held in guest memory, config-driven odds via config_get, and the casino
// wallet over kv_set with sum/max merge rules.
//
// Build: tinygo build -o pokies.wasm -target wasip1 -buildmode=c-shared .
package main

import gamekit "github.com/shellcade/gamekit"

func main() {}

func init() { gamekit.Run(Game{}) }

// The eight ABI exports, trampolined to the gamekit SDK.

//go:export shellcade_abi
func expABI() int32 { return gamekit.ExportABI() }

//go:export meta
func expMeta() int32 { return gamekit.ExportMeta() }

//go:export start
func expStart() int32 { return gamekit.ExportStart() }

//go:export join
func expJoin() int32 { return gamekit.ExportJoin() }

//go:export leave
func expLeave() int32 { return gamekit.ExportLeave() }

//go:export input
func expInput() int32 { return gamekit.ExportInput() }

//go:export wake
func expWake() int32 { return gamekit.ExportWake() }

//go:export close
func expClose() int32 { return gamekit.ExportClose() }

// Game is the pokies registry entry.
type Game struct{}

// Meta returns the static game metadata (mirrors the native pokies meta).
func (Game) Meta() gamekit.GameMeta {
	return gamekit.GameMeta{
		Slug:             "pokies",
		Name:             "Pokies",
		ShortDescription: "Pull the lever on your own slot machine and chase your high score.",
		MinPlayers:       1,
		MaxPlayers:       5,
		Tags:             []string{"slots", "casual"},

		QuickModeLabel:    "Quick spin",
		SoloModeLabel:     "Solo spin",
		PrivateInviteLine: "Friends join your floor when they enter the code.",

		Leaderboard: &gamekit.LeaderboardSpec{
			MetricLabel: "Credits",
			Direction:   gamekit.HigherBetter,
			Aggregation: gamekit.BestResult,
			Format:      gamekit.Integer,
		},
	}
}

// NewRoom returns the per-room behavior.
func (Game) NewRoom(cfg gamekit.RoomConfig, svc gamekit.Services) gamekit.Handler {
	return newRoom(cfg, svc)
}
