//go:build wasip1 || tinygo.wasm

package game

import (
	"math/rand"

	"github.com/shellcade/kit/wire"
)

// Run registers the game with the export trampolines. Call it from an init()
// (or main()) in the plugin's main package; the eight //go:export functions in
// main delegate to the Export* functions below (`shellcade-kit new` scaffolds
// exactly this main package).
//
// One plugin instance == one room, so package-level state is per-room state.
var (
	theGame Game
	handler Handler
	rng     *rand.Rand
)

// Run installs the game. The instance's Handler is created lazily on start.
func Run(g Game) { theGame = g }

// ExportABI backs the shellcade_abi export.
func ExportABI() int32 {
	var b [4]byte
	b[0] = byte(ABIVersion)
	b[1] = byte(ABIVersion >> 8)
	b[2] = byte(ABIVersion >> 16)
	b[3] = byte(ABIVersion >> 24)
	outputBytes(b[:])
	return 0
}

// ExportMeta backs the meta export.
func ExportMeta() int32 {
	outputBytes(encodeMeta(theGame.Meta()))
	return 0
}

// decodeCall decodes the input payload into a Room for this callback.
func decodeCall() (*room, *wire.Rd) {
	ctx, r := decodeCtx(inputBytes())
	if rng == nil {
		rng = rand.New(rand.NewSource(ctx.cfg.Seed))
	}
	return &room{ctx: ctx, rng: rng}, r
}

func decodePlayer(rm *room, r *wire.Rd) (Player, bool) {
	idx := int(r.U32())
	if r.Bad || idx < 0 || idx >= len(rm.ctx.members) {
		return Player{}, false
	}
	return rm.ctx.members[idx], true
}

// ExportStart backs the start export.
func ExportStart() int32 {
	rm, _ := decodeCall()
	handler = theGame.NewRoom(rm.ctx.cfg, rm.Services())
	handler.OnStart(rm)
	return 0
}

// ExportJoin backs the join export.
func ExportJoin() int32 {
	rm, r := decodeCall()
	if p, ok := decodePlayer(rm, r); ok && handler != nil {
		handler.OnJoin(rm, p)
	}
	return 0
}

// ExportLeave backs the leave export.
func ExportLeave() int32 {
	rm, r := decodeCall()
	if p, ok := decodePlayer(rm, r); ok && handler != nil {
		handler.OnLeave(rm, p)
	}
	return 0
}

// ExportInput backs the input export.
func ExportInput() int32 {
	rm, r := decodeCall()
	p, ok := decodePlayer(rm, r)
	if !ok || handler == nil {
		return 0
	}
	var in Input
	in.Kind = InputKind(r.U8())
	in.Rune = rune(r.U32())
	in.Key = Key(r.U8())
	handler.OnInput(rm, p, in)
	return 0
}

// ExportWake backs the wake export (the host heartbeat).
func ExportWake() int32 {
	rm, _ := decodeCall()
	if handler != nil {
		handler.OnWake(rm)
	}
	return 0
}

// ExportClose backs the close export.
func ExportClose() int32 {
	rm, _ := decodeCall()
	if handler != nil {
		handler.OnClose(rm)
	}
	return 0
}

// Main is the dual-target entrypoint: under wasm it registers the game (the
// //go:export trampolines do the rest); built natively it runs the instant
// local dev loop (see devrun.go) — same game source, no wasm in the inner loop.
func Main(g Game) { Run(g) }
