// Package smoke is the engine behind the public smoke package and the dev
// runner's -smoke mode: it parses a game's smoke.yaml, executes the script
// deterministically against a native game (virtual clock, seeded RNG), and
// renders the named shots. The devkit CLI runs the same script against the
// built wasm artifact and renders through the same encoder, so a
// deterministic game produces byte-identical shots on either path.
package smoke

import (
	"time"

	"github.com/shellcade/kit/v2/internal/game"
)

// DefaultHeartbeat is the wake cadence used when smoke.yaml omits heartbeat —
// matching the dev runner's default.
const DefaultHeartbeat = 50 * time.Millisecond

// Script is a parsed smoke.yaml: the deterministic inputs (seed, seats,
// config) plus the ordered steps that drive the game and name the shots.
type Script struct {
	Seed      int64
	Seats     int
	Heartbeat time.Duration
	Config    map[string]string
	Steps     []Step
}

// StepKind enumerates the script step vocabulary.
type StepKind uint8

const (
	// StepRune delivers a printable rune to the current seat (`rune:`, and
	// each character of a `text:` step).
	StepRune StepKind = iota
	// StepKey delivers a named key to the current seat (`key:`).
	StepKey
	// StepSeat switches the current input seat (`seat:`), sticky.
	StepSeat
	// StepAdvance sweeps the virtual clock forward, waking the game once per
	// heartbeat (`advance:`).
	StepAdvance
	// StepWake delivers a single wake without moving the clock (`wake:`).
	StepWake
	// StepShot captures the current screen of every captured seat (`shot:`).
	StepShot
)

// Step is one parsed script step. Line is the smoke.yaml source line, kept
// for error reporting during the run.
type Step struct {
	Kind  StepKind
	Line  int
	Rune  rune          // StepRune
	Key   game.Key      // StepKey
	Seat  int           // StepSeat target
	D     time.Duration // StepAdvance
	Name  string        // StepShot name
	Seats []int         // StepShot filter; nil = all seats
}

// Shot is one captured screen dump: the frames of every captured seat at the
// moment the shot step ran. When every captured frame is identical (broadcast
// games) the shot collapses to a single unsuffixed file.
type Shot struct {
	Ordinal int    // 1-based shot index within the script
	Name    string // the `shot:` name
	Seats   []int  // captured seat indices, ascending
	Frames  []*game.Frame
}

// Collapsed reports whether the shot writes one file: a single captured seat,
// or every captured seat's frame byte-identical.
func (s *Shot) Collapsed() bool {
	if len(s.Frames) <= 1 {
		return true
	}
	for _, f := range s.Frames[1:] {
		if f.Cells != s.Frames[0].Cells {
			return false
		}
	}
	return true
}
