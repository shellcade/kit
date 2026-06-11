//go:build wasip1 || tinygo.wasm

package game

import (
	"math/rand"

	"github.com/shellcade/kit/v2/wire"
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
// The declared ctx features are captured here so the callback decoder reads
// member sections with the shape the host encodes for this guest (the
// character section carries no in-band discriminator). Meta() is read once at
// registration, so it must return a complete value (the kit.Main scaffold
// pattern).
func Run(g Game) {
	theGame = g
	declaredCtxFeatures = g.Meta().CtxFeatures
}

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

// decodeCall decodes the input payload into a Room for this callback. It also
// runs the roster-change backstop (D7): decodeCtx's roster cache compares the
// member section's raw wire bytes against the previous callback's; on any
// change (join/leave/index-shift) every per-index baseline is invalidated so
// the next send to each slot is a keyframe. This is the host-authority
// backstop, not the primary resync (the host's epoch bump is) — but it keeps
// the guest's baselines from diffing across a roster renumber. The byte
// compare is strictly stronger than the fingerprint hash it replaced, and on
// an unchanged roster the callback decodes ZERO member strings (allocation-
// free — load-bearing under -gc=leaking, where per-callback roster decodes
// leak at callback rate and OOM long-lived large rooms).
func decodeCall() (*room, *wire.Rd) {
	ctx, r, rosterChanged := decodeCtx(inputBytes())
	if rng == nil {
		rng = rand.New(rand.NewSource(ctx.cfg.Seed))
	}
	if rosterChanged {
		invalidateBaselines()
	}
	rm := &room{ctx: ctx, rng: rng}
	if epochMismatch && !epochMismatchLogged {
		// Host fault: an unchanged-form ctx carried an epoch we don't hold.
		// Degraded (cached roster kept, baselines invalidated) — warn once.
		epochMismatchLogged = true
		rm.Log("kit: ctx roster epoch mismatch (host fault); using cached roster")
	}
	epochMismatch = false
	return rm, r
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

// ExportInput backs the input export. Per the v2 tolerant-reader rule, an input
// whose kind or named key the guest does not recognise is SILENTLY IGNORED (no
// fault, no callback) — future input growth (mouse, paste, focus, new keys)
// extends this enum additively without breaking this artifact. The wire decoder
// already tolerates trailing payload bytes beyond the fields read here.
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
	if r.Bad || !knownInput(in) {
		return 0 // unknown kind/key (or short read): ignore, no callback
	}
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
