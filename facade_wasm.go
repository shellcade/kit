//go:build wasip1 || tinygo.wasm

package kit

import "github.com/shellcade/kit/internal/game"

// Run registers the game for the wasm build; Main is the dual-target
// entrypoint (under wasm it just registers — the trampolines do the rest).
func Run(g Game)  { game.Run(g) }
func Main(g Game) { game.Run(g) }

// The eight export bodies the game's //go:export trampolines delegate to.
func ExportABI() int32   { return game.ExportABI() }
func ExportMeta() int32  { return game.ExportMeta() }
func ExportStart() int32 { return game.ExportStart() }
func ExportJoin() int32  { return game.ExportJoin() }
func ExportLeave() int32 { return game.ExportLeave() }
func ExportInput() int32 { return game.ExportInput() }
func ExportWake() int32  { return game.ExportWake() }
func ExportClose() int32 { return game.ExportClose() }
