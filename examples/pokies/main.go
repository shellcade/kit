// Pokies — the shellcade devkit reference game: a wake-driven port of the
// native pokies cabinet to the wasm ABI (gamekit). Demonstrates the canonical
// idioms: a reel animation derived from CallContext time, one-shot deadlines
// held in guest memory, config-driven odds via config_get, and the casino
// wallet over kv_set with sum/max merge rules.
//
// Build: tinygo build -o pokies.wasm -target wasip1 -buildmode=c-shared .
package main

import kit "github.com/shellcade/kit"

func main() { kit.Main(Game{}) }

// Game is the pokies registry entry.
type Game struct{}

// Meta returns the static game metadata (mirrors the native pokies meta).
func (Game) Meta() kit.GameMeta {
	return kit.GameMeta{
		Slug:             "pokies",
		Name:             "Pokies",
		ShortDescription: "Pull the lever on your own slot machine and chase your high score.",
		MinPlayers:       1,
		MaxPlayers:       5,
		Tags:             []string{"slots", "casual"},

		QuickModeLabel:    "Quick spin",
		SoloModeLabel:     "Solo spin",
		PrivateInviteLine: "Friends join your floor when they enter the code.",

		Leaderboard: &kit.LeaderboardSpec{
			MetricLabel: "Credits",
			Direction:   kit.HigherBetter,
			Aggregation: kit.BestResult,
			Format:      kit.Integer,
		},
	}
}

// NewRoom returns the per-room behavior.
func (Game) NewRoom(cfg kit.RoomConfig, svc kit.Services) kit.Handler {
	return newRoom(cfg, svc)
}
