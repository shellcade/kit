//go:build !wasip1 && !tinygo.wasm

package kit

import (
	"os"

	"github.com/shellcade/kit/v2/internal/game"
	internalsmoke "github.com/shellcade/kit/v2/internal/smoke"
)

// Main, built natively, runs the instant inner-loop dev runner: `go run .`
// plays the game in this terminal with normal Go tooling and zero wasm.
// Flags: -seed N · -heartbeat 50ms · -config k=v · -seats N · -handle name.
//
// With -smoke <file> [-smoke-out <dir>] it instead runs the smoke script
// non-interactively and writes the named shot files — see the smoke package
// and GUIDE.md "Smoke scripts".
func Main(g Game) {
	if internalsmoke.Wants(os.Args[1:]) {
		internalsmoke.MainCLI(g, os.Args[1:])
		return
	}
	game.Main(g)
}
