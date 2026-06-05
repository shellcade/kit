// Pokies — the shellcade devkit reference game: a wake-driven port of the
// native pokies cabinet to the wasm ABI (gamekit). Demonstrates the canonical
// idioms: a reel animation derived from CallContext time, one-shot deadlines
// held in guest memory, config-driven odds via config_get, and the casino
// wallet over kv_set with sum/max merge rules.
//
// This is the thin entrypoint: the game logic lives in the importable
// examples/pokies/pokies package so it can be driven natively (e.g. for an
// in-process comparison against this wasm build).
//
// Build: tinygo build -o pokies.wasm -target wasip1 -buildmode=c-shared .
package main

import (
	kit "github.com/shellcade/kit"
	"github.com/shellcade/kit/examples/pokies/pokies"
)

func main() { kit.Main(pokies.Game{}) }
