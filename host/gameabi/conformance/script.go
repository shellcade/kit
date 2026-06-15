// Package conformance runs a scripted scenario against a wasm game through the
// REAL gameabi adapter (limits ON) and reports per-callback latency, exit codes,
// frames, and peak linear memory, plus budget verdicts that name the breached
// limit, the measured value, and the step that breached it. It is the shared
// engine behind `shellcade-kit check` and is importable by internal/catalog for
// release-intake gating.
//
// The harness drives the handler's callbacks SYNCHRONOUSLY against an
// instrumented Room (not the live actor goroutine): synchronous control is what
// lets it sample guest memory after each callback, advance a virtual clock in
// fixed steps, and snapshot/restore mid-script for the hibernation-determinism
// check — none of which the async RoomCtl surface exposes. The adapter, limits,
// virtualized WASI, and host functions exercised are the production ones.
package conformance

// StepKind tags a scripted step.
type StepKind uint8

const (
	StepJoin StepKind = iota
	StepLeave
	StepInput
	StepWake
	StepAdvance
	StepSnapshotRestore
	StepShot // capture marker for RunShots; a no-op under Run
)

// Step is one scripted action. Construct steps with the helpers below rather than
// building this struct directly.
type Step struct {
	Kind StepKind

	Seat int        // seat index for Join/Leave/Input (0-based)
	Rune rune       // for StepInput (rune input; 0 means a key input)
	Key  uint8      // for StepInput (key code when Rune == 0)
	Dur  DurationMS // for StepAdvance
	Note string     // human-readable label echoed in the report

	Name  string // for StepShot: the shot name
	Seats []int  // for StepShot: captured seats, ascending (nil = all members)
}

// DurationMS is a millisecond duration carried in the script (kept simple so a
// script literal reads cleanly and so JSON/flag wiring stays trivial later).
type DurationMS int64

// Join admits the player at seat into the room.
func Join(seat int) Step { return Step{Kind: StepJoin, Seat: seat, Note: "join seat"} }

// Leave removes the player at seat.
func Leave(seat int) Step { return Step{Kind: StepLeave, Seat: seat, Note: "leave seat"} }

// Input delivers a rune to the player at seat.
func Input(seat int, r rune) Step {
	return Step{Kind: StepInput, Seat: seat, Rune: r, Note: "input rune"}
}

// Key delivers a key code to the player at seat.
func Key(seat int, key uint8) Step {
	return Step{Kind: StepInput, Seat: seat, Key: key, Note: "input key"}
}

// Wake fires one host-heartbeat wake.
func Wake() Step { return Step{Kind: StepWake, Note: "wake"} }

// Advance moves the virtual clock forward by ms milliseconds (the next callback
// reads the new clock as its CallContext time).
func Advance(ms int64) Step {
	return Step{Kind: StepAdvance, Dur: DurationMS(ms), Note: "advance clock"}
}

// SnapshotRestore checkpoints the room: snapshot the current state, restore into
// a fresh handler, and continue the script against the restored room. The runner
// also verifies the post-checkpoint frames match an uninterrupted control
// (hibernation determinism).
func SnapshotRestore() Step { return Step{Kind: StepSnapshotRestore, Note: "snapshot/restore"} }

// Shot marks a capture point for RunShots: the latest frame of each listed
// seat (nil = every member) is recorded under name. Run ignores shot steps.
func Shot(name string, seats []int) Step {
	return Step{Kind: StepShot, Name: name, Seats: seats, Note: "shot"}
}

// Script is an ordered list of steps.
type Script []Step
