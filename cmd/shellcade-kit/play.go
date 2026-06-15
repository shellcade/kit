package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/render"
	"github.com/shellcade/kit/v2/host/sdk"
	"github.com/shellcade/kit/v2/host/session"
)

// play runs the artifact in a local interactive 80x24 room: real engine, real
// renderer, raw-mode stdin/stdout, in-memory services. The argument may be a
// built .wasm or the game directory — a directory is built first, which makes
// `shellcade-kit play .` the whole edit-see loop for Rust authors (who have
// no native runner) and Go authors without a tinygo invocation memorized.
//
// Multiplayer testing is hot-seat: --seats N joins N players to the one room;
// your keyboard controls the ACTIVE seat and Ctrl-T cycles seats, so you can
// drive both sides of a duel (or all five pokies cabinets) from one terminal.
// Each seat keeps its own per-player frame stream — switching seats shows that
// seat's latest frame, which exercises per-viewer composition for real.
func play(arg string, args []string) error {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	seed := fs.Int64("seed", 0, "room RNG seed (0 = time-based)")
	heartbeat := fs.Duration("heartbeat", gameabiHeartbeat, "wake cadence")
	seats := fs.Int("seats", 1, "players joined to the room; Ctrl-T switches the active seat")
	cfgVals := configFlags{}
	fs.Var(cfgVals, "config", "KEY=VALUE per-game config (repeatable; value may be @file)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seats < 1 {
		*seats = 1
	}

	// Build-or-passthrough BEFORE touching the terminal, so compiler output
	// lands on a normal screen and a build failure exits cleanly.
	path, _, cleanup, err := resolveArtifact(arg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	game, ctl, err := newRoom(path, *seed, *seed != 0, *heartbeat, cfgVals, log)
	if err != nil {
		return err
	}
	if max := game.Meta().MaxPlayers; *seats > max {
		*seats = max
	}

	players := make([]sdk.Player, *seats)
	for i := range players {
		// A zero character keeps local hot-seat play dependency-free; the
		// public devkit doesn't resolve account default characters.
		players[i] = sdk.Player{
			AccountID: fmt.Sprintf("seat-%d", i+1),
			Handle:    fmt.Sprintf("seat%d", i+1),
			Kind:      sdk.KindMember,
			Conn:      fmt.Sprintf("local-%d", i+1),
		}
		if err := ctl.Join(players[i]); err != nil {
			return fmt.Errorf("join seat %d: %w", i+1, err)
		}
	}

	// Terminal: raw mode + alt screen + hidden cursor.
	fd := os.Stdin.Fd()
	state, err := xterm.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode (need a real terminal): %w", err)
	}
	restore := func() {
		_ = xterm.Restore(fd, state)
		fmt.Print("\x1b[?25h\x1b[?1049l")
	}
	defer restore()
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[2J")

	caps := session.Caps{ColorDepth: session.ColorTrue, UTF8: true}
	termCols, termRows := 80, 24
	if c, r, err := xterm.GetSize(fd); err == nil && c > 0 {
		termCols, termRows = c, r
	}

	// Input pump: raw bytes -> sdk.Input for the ACTIVE seat; Ctrl-T cycles
	// seats; Esc/Ctrl-C leaves.
	seatCh := make(chan int, 4) // seat-switch requests
	inputs := make(chan sdk.Input, 16)
	quit := make(chan struct{})
	go func() {
		defer close(quit)
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			i := 0
			for i < n {
				b := buf[i]
				switch {
				case b == 0x03: // Ctrl-C
					return
				case b == 0x14: // Ctrl-T: next seat
					seatCh <- 1
				case b == 0x1b:
					if i+2 < n && buf[i+1] == '[' {
						switch buf[i+2] {
						case 'A':
							inputs <- sdk.KeyInput(sdk.KeyUp)
						case 'B':
							inputs <- sdk.KeyInput(sdk.KeyDown)
						case 'C':
							inputs <- sdk.KeyInput(sdk.KeyRight)
						case 'D':
							inputs <- sdk.KeyInput(sdk.KeyLeft)
						}
						i += 3
						continue
					}
					return // bare Esc: leave
				case b == '\r' || b == '\n':
					inputs <- sdk.KeyInput(sdk.KeyEnter)
				case b == 0x7f:
					inputs <- sdk.KeyInput(sdk.KeyBackspace)
				case b == '\t':
					inputs <- sdk.KeyInput(sdk.KeyTab)
				case b >= 0x20:
					inputs <- sdk.RuneInput(rune(b))
				}
				i++
			}
		}
	}()

	// Frame pumps: one per seat; latest frame kept per seat, active rendered.
	type seatFrame struct {
		seat int
		g    canvas.Grid
		ok   bool
	}
	frames := make(chan seatFrame, *seats*2)
	for i, p := range players {
		i, p := i, p
		go func() {
			ch := ctl.Frames(p)
			for g := range ch {
				frames <- seatFrame{seat: i, g: g, ok: true}
			}
			frames <- seatFrame{seat: i}
		}()
	}

	last := make([]canvas.Grid, *seats)
	have := make([]bool, *seats)
	active := 0
	closedSeats := 0

	draw := func() {
		if !have[active] {
			return
		}
		body := render.GridToANSI(last[active], caps)
		if termCols > canvas.Cols || termRows > canvas.Rows {
			body = render.Letterbox(body, termCols, termRows, caps.ColorDepth)
		}
		out := "\x1b[H" + strings.ReplaceAll(body, "\n", "\r\n")
		if *seats > 1 && termRows > canvas.Rows {
			status := fmt.Sprintf(" seat %d/%d — Ctrl-T switches ", active+1, *seats)
			out += fmt.Sprintf("\x1b[%d;%dH\x1b[2m%s\x1b[0m", termRows, max(1, (termCols-len(status))/2), status)
		}
		_, _ = os.Stdout.WriteString(out)
	}

	for {
		select {
		case f := <-frames:
			if !f.ok {
				closedSeats++
				if closedSeats >= *seats {
					return nil // room settled
				}
				continue
			}
			last[f.seat], have[f.seat] = f.g, true
			if f.seat == active {
				draw()
			}
		case <-seatCh:
			active = (active + 1) % *seats
			draw()
		case in := <-inputs:
			ctl.Input(players[active], in)
		case <-quit:
			for _, p := range players {
				ctl.Leave(p)
			}
			select {
			case <-ctl.Done():
			case <-time.After(time.Second):
			}
			return nil
		case <-ctl.Done():
			return nil
		}
	}
}

// gameabiHeartbeat mirrors gameabi.Heartbeat for the flag default.
const gameabiHeartbeat = 50 * time.Millisecond
