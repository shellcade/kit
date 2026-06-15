package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	kit "github.com/shellcade/kit/v2"
	kitsmoke "github.com/shellcade/kit/v2/smoke"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
)

// runSmoke is `shellcade-kit smoke <gamedir|game.wasm> [--out dir]`: run the
// game's smoke.yaml against the built wasm artifact through the conformance
// harness and write the named shot files. This is the canonical smoke path —
// the script semantics, room shape, clock epoch, and renderer are the kit
// smoke contract, so the files are byte-identical to `go run . -smoke` for a
// deterministic game (and it is the only path for non-Go games).
func runSmoke(arg string, rest []string) error {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	out := fs.String("out", "smoke-out", "directory for shot files")
	script := fs.String("script", "", "smoke script (default: smoke.yaml next to the game)")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	wasm, dir, cleanup, err := resolveArtifact(arg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	scriptPath := *script
	if scriptPath == "" {
		scriptPath = filepath.Join(dir, "smoke.yaml")
	}
	b, err := os.ReadFile(scriptPath)
	if err != nil {
		return err
	}
	sc, err := kitsmoke.Parse(b)
	if err != nil {
		return err
	}
	shots, err := runWasmSmoke(wasm, sc)
	if err != nil {
		return err
	}
	names, err := kitsmoke.WriteShots(*out, shots)
	if err != nil {
		return err
	}
	for _, n := range names {
		fmt.Println(n)
	}
	fmt.Printf("smoke: %d shots → %s\n", len(shots), *out)
	return nil
}

// runWasmSmoke executes a parsed smoke script against a wasm artifact and
// returns kit shots (frames converted from the host grid form).
func runWasmSmoke(wasm string, sc *kitsmoke.Script) ([]kitsmoke.Shot, error) {
	// The conformance clock carries millisecond steps; a finer heartbeat
	// cannot round-trip the wasm path identically, so refuse it rather than
	// quietly diverge from the native runner.
	if sc.Heartbeat%time.Millisecond != 0 {
		return nil, fmt.Errorf("smoke: heartbeat %s must be a whole number of milliseconds", sc.Heartbeat)
	}
	beatMS := int64(sc.Heartbeat / time.Millisecond)

	// Replay the script's sticky-seat semantics into explicit conformance
	// steps; `advance` expands to one (clock step + wake) per heartbeat —
	// exactly the native runner's sweep.
	var steps conformance.Script
	cur := 0
	for _, st := range sc.Steps {
		switch st.Kind {
		case kitsmoke.StepRune:
			steps = append(steps, conformance.Input(cur, st.Rune))
		case kitsmoke.StepKey:
			steps = append(steps, conformance.Key(cur, uint8(st.Key)))
		case kitsmoke.StepSeat:
			cur = st.Seat
		case kitsmoke.StepAdvance:
			for n := int64(0); n < int64(st.D/sc.Heartbeat); n++ {
				steps = append(steps, conformance.Advance(beatMS), conformance.Wake())
			}
		case kitsmoke.StepWake:
			steps = append(steps, conformance.Wake())
		case kitsmoke.StepShot:
			seats := st.Seats
			if seats != nil {
				seats = append([]int(nil), seats...)
				for i := 1; i < len(seats); i++ { // small insertion sort, ascending
					for j := i; j > 0 && seats[j] < seats[j-1]; j-- {
						seats[j], seats[j-1] = seats[j-1], seats[j]
					}
				}
			}
			steps = append(steps, conformance.Shot(st.Name, seats))
		}
	}

	frames, err := conformance.RunShots(wasm, gameabi.Options{}, conformance.SmokeRun{
		Seed:   sc.Seed,
		Seats:  sc.Seats,
		Config: sc.Config,
		Epoch:  kitsmoke.SeedEpoch(sc.Seed),
		Script: steps,
	})
	if err != nil {
		return nil, err
	}

	shots := make([]kitsmoke.Shot, len(frames))
	for i, sf := range frames {
		shots[i] = kitsmoke.Shot{Ordinal: i + 1, Name: sf.Name, Seats: sf.Seats}
		for _, g := range sf.Frames {
			shots[i].Frames = append(shots[i].Frames, gridToKitFrame(g))
		}
	}
	return shots, nil
}

// gridToKitFrame converts the host canvas grid to the kit frame form, cell by
// cell (Rune/Cp2/Cp3/FG/BG/Attr/Cont map 1:1), so shots render through kit's
// canonical encoder.
func gridToKitFrame(g canvas.Grid) *kit.Frame {
	f := &kit.Frame{}
	for r := 0; r < canvas.Rows; r++ {
		for c := 0; c < canvas.Cols; c++ {
			cell := g.Cells[r][c]
			f.Cells[r][c] = kit.Cell{
				Rune: cell.Rune,
				Cp2:  cell.Cp2,
				Cp3:  cell.Cp3,
				FG:   kitColor(cell.FG),
				BG:   kitColor(cell.BG),
				Attr: kit.Attr(cell.Attr),
				Cont: cell.Cont,
			}
		}
	}
	return f
}

func kitColor(c canvas.Color) kit.Color {
	if !c.IsSet() {
		return kit.Color{}
	}
	r, g, b := c.RGB()
	return kit.RGB(r, g, b)
}
