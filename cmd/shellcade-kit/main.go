// Command shellcade-kit is the shellcade game developer kit (PROTOTYPE):
//
//	shellcade-kit version                              print kit/ABI compatibility info
//	shellcade-kit new   [--rust] [--license ID] <name> scaffold a complete, catalog-submittable kit game
//	shellcade-kit check <gamedir|game.wasm>            run the conformance harness (limits ON) + print a report
//	shellcade-kit play  <gamedir|game.wasm> [flags]    play the game in a local 80x24 terminal room
//	shellcade-kit smoke <gamedir|game.wasm>            run the game's smoke.yaml and write the shot files
//
// check/play/smoke accept either a built .wasm or the game directory — a
// directory is built first (TinyGo for go.mod, cargo wasm32-wasip1 for
// Cargo.toml), so `shellcade-kit play .` is the whole inner loop for any
// source language.
//
// play flags:
//
//	--seed N            seed the room RNG (reproducible runs)
//	--heartbeat DUR     wake cadence (default 50ms)
//	--config KEY=VALUE  inject a per-game config value (repeatable; value may be @file)
//	--players N         scripted extra players that join alongside you (default 0)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/gameabi/conformance"
	"github.com/shellcade/kit/v2/host/memsvc"
	"github.com/shellcade/kit/v2/host/sdk"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		printVersion()
		return
	}
	if len(os.Args) < 3 {
		usage()
	}
	cmd, path := os.Args[1], os.Args[2]
	switch cmd {
	case "new":
		// new [--rust] [--license ID] <name>: flags come before the name.
		fs := flag.NewFlagSet("new", flag.ExitOnError)
		rust := fs.Bool("rust", false, "scaffold a Rust game (default: Go)")
		license := fs.String("license", "MIT", "LICENSE to emit; one of the catalog allowlist: "+strings.Join(licenseIDs(), ", "))
		if err := fs.Parse(os.Args[2:]); err != nil {
			usage()
		}
		if fs.NArg() != 1 {
			usage()
		}
		name := strings.ToLower(fs.Arg(0))
		if err := runNew(name, *rust, *license); err != nil {
			fmt.Fprintln(os.Stderr, "shellcade-kit:", err)
			os.Exit(1)
		}
		if *rust {
			fmt.Printf("Scaffolded %s/ — try it now:\n\n  rustup target add wasm32-wasip1   # once\n  cd %s && cargo test && shellcade-kit play .\n", name, name)
		} else {
			fmt.Printf("Scaffolded %s/ — try it now:\n\n  cd %s && go mod tidy && go run .\n", name, name)
		}
	case "check":
		// check [--require-leaderboard] <gamedir|game.wasm>
		fs := flag.NewFlagSet("check", flag.ExitOnError)
		requireLB := fs.Bool("require-leaderboard", false, "also fail unless the game declares a leaderboard (catalog publishing policy)")
		if err := fs.Parse(os.Args[2:]); err != nil {
			usage()
		}
		if fs.NArg() != 1 {
			usage()
		}
		if err := check(fs.Arg(0), *requireLB); err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
	case "meta":
		if err := printMeta(path); err != nil {
			fmt.Fprintln(os.Stderr, "shellcade-kit:", err)
			os.Exit(1)
		}
	case "play":
		if err := play(path, os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, "shellcade-kit:", err)
			os.Exit(1)
		}
	case "smoke":
		if err := runSmoke(path, os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, "shellcade-kit:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

// printVersion reports the three version facts from build info: this binary's
// version, the kit module it was built against, and the ABI major it enforces.
// The ABI major is the only one that MUST match an artifact (the load handshake
// enforces it); the kit version answers "which SDK release does this verify?".
func printVersion() {
	own := "(devel)"
	kitv := "(unknown)"
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" {
			own = v
		}
		for _, d := range bi.Deps {
			if d.Path == "github.com/shellcade/kit/v2" {
				kitv = d.Version
			}
		}
	}
	fmt.Printf("shellcade-kit %s\nkit           %s (github.com/shellcade/kit/v2)\nabi           v%d\n", own, kitv, gameabi.Version)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shellcade-kit version | new [--rust] [--license ID] <name> | check <gamedir|game.wasm> | meta <game.wasm> | play <gamedir|game.wasm> [flags] | smoke <gamedir|game.wasm> [--out dir]")
	os.Exit(2)
}

// printMeta loads the artifact (full ABI handshake) and prints its decoded
// metadata as JSON — the machine-readable source of truth catalog CI asserts
// against (dir name == bare meta slug; player bounds; display fields). The
// artifact is the ONLY place game metadata lives; there is no manifest file.
func printMeta(path string) error {
	game, err := gameabi.LoadGame(path, gameabi.Options{})
	if err != nil {
		return err
	}
	out := struct {
		ABI uint32 `json:"abi"`
		sdk.GameMeta
	}{gameabi.Version, game.Meta()}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// configFlags collects repeated --config KEY=VALUE flags.
type configFlags map[string]string

func (c configFlags) String() string { return "" }
func (c configFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("--config wants KEY=VALUE, got %q", v)
	}
	if strings.HasPrefix(val, "@") {
		b, err := os.ReadFile(val[1:])
		if err != nil {
			return err
		}
		val = string(b)
	}
	c[k] = val
	return nil
}

// newRoom loads the artifact and builds a live engine room around it with the
// in-memory services factory (the same double serve --dev style tests use).
func newRoom(path string, seed int64, seedSet bool, heartbeat time.Duration, cfgVals map[string]string, log *slog.Logger) (sdk.Game, sdk.RoomCtl, error) {
	game, err := gameabi.LoadGame(path, gameabi.Options{Heartbeat: heartbeat})
	if err != nil {
		return nil, nil, err
	}
	meta := game.Meta()

	reg := sdk.NewRegistry()
	if err := reg.Add(game); err != nil {
		return nil, nil, err
	}
	factory := memsvc.NewFactory(log, reg)
	for k, v := range cfgVals {
		factory.SetConfig(meta.Slug, k, []byte(v))
	}
	svc := factory.For("devkit", meta.Slug)

	cfg := sdk.RoomConfig{
		Mode:       sdk.ModePrivate,
		Capacity:   meta.MaxPlayers,
		MinPlayers: meta.MinPlayers,
		Seed:       seed,
		SeedSet:    seedSet,
	}
	ctl := sdk.NewRoomRuntime("devkit", game.NewRoom(cfg, svc), cfg, svc)
	return game, ctl, nil
}

// ---- check -------------------------------------------------------------------

// check runs the full conformance harness against an artifact with a default
// scripted scenario — all exports exercised, a two-seat roster sequence, and a
// hibernation-determinism checkpoint — and prints a human-readable report. It
// returns an error (non-zero exit) when any budget verdict fails. The
// argument may be a built .wasm or the game directory (built first), so
// `shellcade-kit check .` is the one-command merge-gate rehearsal for any
// source language.
func check(arg string, requireLeaderboard bool) error {
	path, _, cleanup, err := resolveArtifact(arg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Probe the meta so the default script can size the roster to the game.
	game, err := gameabi.LoadGame(path, gameabi.Options{})
	if err != nil {
		return err
	}
	meta := game.Meta()

	rep, err := conformance.Run(path, gameabi.Options{}, defaultCheckScript(meta))
	if err != nil {
		return err
	}
	// Catalog publishing policy (opt-in): every published game must declare a
	// leaderboard. Not part of generic ABI conformance, so minimal fixtures and
	// `shellcade-kit check` without the flag are unaffected.
	if requireLeaderboard {
		rep.Verdicts = append(rep.Verdicts, conformance.LeaderboardVerdict(meta))
	}
	printReport(rep)
	if !rep.Pass() {
		return fmt.Errorf("%d budget verdict(s) failed", countFailing(rep))
	}
	return nil
}

// defaultCheckScript drives every export against any game: join up to two seats,
// a spread of generic inputs and host-heartbeat wakes with clock advances, a
// snapshot/restore checkpoint (hibernation determinism), then a leave + rejoin.
func defaultCheckScript(meta sdk.GameMeta) conformance.Script {
	seats := 1
	if meta.MaxPlayers >= 2 {
		seats = 2
	}
	s := conformance.Script{conformance.Join(0)}
	if seats == 2 {
		s = append(s, conformance.Join(1))
	}
	// Generic inputs every game tolerates (Enter, space) interleaved across seats.
	s = append(s,
		conformance.Key(0, uint8(sdk.KeyEnter)),
		conformance.Wake(),
		conformance.Advance(50),
		conformance.Wake(),
	)
	if seats == 2 {
		s = append(s, conformance.Input(1, ' '), conformance.Wake())
	}
	s = append(s,
		conformance.SnapshotRestore(), // hibernation checkpoint
		conformance.Input(0, ' '),
		conformance.Advance(50),
		conformance.Wake(),
	)
	if seats == 2 {
		s = append(s,
			conformance.Leave(1),
			conformance.Wake(),
			conformance.Join(1), // rejoin
			conformance.Wake(),
		)
	}
	return s
}

// printReport renders a conformance Report: header, the verdict-per-requirement
// list, the per-callback latency/memory table, and any named-budget failures.
func printReport(rep conformance.Report) {
	m := rep.Meta
	fmt.Printf("abi:   v%d\n", rep.ABIVersion)
	fmt.Printf("game:  %s (%q) players %d-%d\n", m.Slug, m.Name, m.MinPlayers, m.MaxPlayers)
	fmt.Printf("peak:  %s linear memory (cap %s)   deadline %s\n\n",
		humanBytes(rep.PeakMem), humanBytes(rep.MemCap), rep.Deadline)

	fmt.Println("verdicts:")
	for _, v := range rep.Verdicts {
		mark := "PASS"
		if !v.OK {
			mark = "FAIL"
		}
		fmt.Printf("  [%s] %-22s limit=%-10s measured=%s\n", mark, v.Name, dash(v.Limit), dash(v.Measured))
		if !v.OK {
			if v.Step >= 0 {
				fmt.Printf("         ^ breached at step %d\n", v.Step)
			}
			if v.Detail != "" {
				fmt.Printf("         %s\n", v.Detail)
			}
		}
	}

	fmt.Printf("\nper-callback (latency / mem / frames):\n")
	for _, st := range rep.Steps {
		if st.Callback == "" {
			continue // non-callback steps (advance/checkpoint) carry no latency
		}
		flag := ""
		if st.Faulted {
			flag = "  <-- FAULT"
		}
		fmt.Printf("  %-26s %10s  %8s  %2d frames%s\n",
			truncate(st.Desc, 26), st.Latency.Round(time.Microsecond), humanBytes(uint64(st.MemBytes)), st.Frames, flag)
	}

	if rep.HibernationChecked {
		mark := "PASS"
		if !rep.HibernationOK {
			mark = "FAIL"
		}
		fmt.Printf("\nhibernation determinism: %s\n", mark)
	}

	fmt.Println()
	if rep.Pass() {
		fmt.Println("check: OK — all budgets within limits")
	} else {
		fmt.Printf("check: FAIL — %d verdict(s) breached (see above)\n", countFailing(rep))
	}
}

func countFailing(rep conformance.Report) int {
	n := 0
	for _, v := range rep.Verdicts {
		if !v.OK {
			n++
		}
	}
	return n
}

func humanBytes(b uint64) string {
	switch {
	case b == 0:
		return "0"
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KiB", b/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
