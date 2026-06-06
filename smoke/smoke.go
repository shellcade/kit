// Package smoke runs a game's smoke.yaml: a small deterministic script
// (seed, seats, steps) that drives the game on a virtual clock and dumps
// named 80×24 screens. The same script powers three surfaces:
//
//   - `go run . -smoke smoke.yaml` — the native inner loop (kit.Main
//     dispatches here; no TinyGo, no terminal needed)
//   - `shellcade-kit smoke <gamedir>` — the canonical path: the same script
//     against the built wasm artifact, rendered through this package's
//     encoder, so a deterministic game produces byte-identical shots
//   - the games catalog CI, which renders the shots as PR previews
//
// A smoke.yaml looks like:
//
//	seed: 42        # required — room RNG seed (deterministic)
//	seats: 2        # required, 1..8 — seats joined before the first step
//	heartbeat: 50ms # optional — wake cadence during `advance`
//	config:         # optional — per-game config values
//	  variant: classic
//	steps:
//	  - shot: lobby       # dump the current screen(s), named "lobby"
//	  - rune: "5"         # printable rune → current seat
//	  - key: enter        # named key: enter/backspace/esc/tab/arrows/space
//	  - text: "hello"     # sugar: one rune step per character
//	  - seat: 1           # switch the current input seat (sticky)
//	  - advance: 1.5s     # sweep the clock, waking once per heartbeat
//	  - wake:             # a single wake without moving the clock
//	  - shot: reveal
//	    seats: [0, 1]     # optional filter; default = all seats
//
// Shots default to every seat and collapse to one file when all captured
// frames are identical (broadcast games). Two runs of the same script against
// the same game produce byte-identical output — `advance` is the only clock,
// the RNG is seeded, and all seats are joined up-front (seat i is AccountID
// "seat-<i>", Handle "seat<i>"). See GUIDE.md "Smoke scripts" for authoring
// guidance, including picking quiescent or exact-tick moments in animated
// games.
package smoke

import (
	kit "github.com/shellcade/kit/v2"
	internal "github.com/shellcade/kit/v2/internal/smoke"
)

// Script is a parsed smoke.yaml.
type Script = internal.Script

// Step is one parsed script step (text: steps are pre-expanded to runes).
type Step = internal.Step

// Shot is one captured screen dump: the frames of every captured seat.
type Shot = internal.Shot

// StepKind enumerates the script step vocabulary.
type StepKind = internal.StepKind

const (
	StepRune    = internal.StepRune
	StepKey     = internal.StepKey
	StepSeat    = internal.StepSeat
	StepAdvance = internal.StepAdvance
	StepWake    = internal.StepWake
	StepShot    = internal.StepShot
)

// DefaultHeartbeat is the wake cadence when smoke.yaml omits heartbeat.
const DefaultHeartbeat = internal.DefaultHeartbeat

// Parse decodes and validates a smoke.yaml; errors name the offending line.
func Parse(b []byte) (*Script, error) { return internal.Parse(b) }

// Run executes a parsed script against the game natively on a virtual-clock,
// seeded room, returning the captured shots in script order.
func Run(g kit.Game, s *Script) ([]Shot, error) { return internal.Run(g, s) }

// RenderANSI renders a frame to the canonical shot byte form: 24 truecolor
// ANSI lines, LF separators, trailing newline.
func RenderANSI(f *kit.Frame) []byte { return internal.RenderANSI(f) }

// RenderText renders a frame as plain text (full grapheme clusters, no escape
// sequences, trailing blanks trimmed) — the greppable twin of RenderANSI.
func RenderText(f *kit.Frame) []byte { return internal.RenderText(f) }

// WriteShots writes each shot's .ansi and .txt files into dir:
// NN-<name>.{ansi,txt} when the shot collapses (single seat or identical
// frames), NN-<name>.seat<K>.{ansi,txt} otherwise. Returns the file names.
func WriteShots(dir string, shots []Shot) ([]string, error) {
	return internal.WriteShots(dir, shots)
}
