//go:build !wasip1 && !tinygo.wasm

package gamekit

import "github.com/shellcade/gamekit/internal/game"

// Main, built natively, runs the instant inner-loop dev runner: `go run .`
// plays the game in this terminal with normal Go tooling and zero wasm.
// Flags: -seed N · -heartbeat 50ms · -config k=v · -seats N · -handle name.
func Main(g Game) { game.Main(g) }
