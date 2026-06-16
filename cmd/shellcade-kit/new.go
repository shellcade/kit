package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/shellcade/kit/v2/host/gameabi"
)

// runNew scaffolds a complete, catalog-submittable kit game in ./<name>/ —
// the same shape the published games catalog (github.com/shellcade/games)
// requires: sources, LICENSE (allowlisted; MIT default), and a working
// smoke.yaml. Scaffolds pin the kit version THIS binary was built against
// (drift-proof by construction): the Go path pins the module version, the
// Rust path pins the matching kit release tag as a cargo git dependency.
func runNew(name string, rust bool, license string) error {
	name = strings.ToLower(name)
	lic, err := licenseText(license, name)
	if err != nil {
		return err
	}
	if rust {
		return scaffoldRust(name, lic)
	}
	return scaffold(name, lic)
}

// validName enforces the platform's bare-name slug rule at scaffold time —
// the SAME validator the wasm loader applies to a declared meta.slug — so an
// invalid name fails here, with the loader's own error text, instead of at
// the first `shellcade-kit check` after a game has been built around it.
func validName(name string) error {
	if err := gameabi.ValidateBareName(name); err != nil {
		return err
	}
	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("%s already exists", name)
	}
	return nil
}

func writeScaffold(name string, files map[string]string) error {
	if err := os.MkdirAll(name, 0o755); err != nil {
		return err
	}
	for f, content := range files {
		p := filepath.Join(name, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func scaffold(name, license string) error {
	if err := validName(name); err != nil {
		return err
	}
	gomod := strings.ReplaceAll(tmplGoMod, "NAME", name)
	gomod = strings.ReplaceAll(gomod, "KITVERSION", kitVersion())
	return writeScaffold(name, map[string]string{
		"main.go":    strings.ReplaceAll(tmplMain, "NAME", name),
		"exports.go": tmplExports,
		"go.mod":     gomod,
		"README.md":  strings.ReplaceAll(tmplReadme, "NAME", name),
		"smoke.yaml": tmplSmoke,
		"LICENSE":    license,
	})
}

// scaffoldRust scaffolds the Rust equivalent: a cdylib crate on the
// shellcade-kit Rust SDK, pinned to this binary's kit release tag. The
// generated README carries the EXACT artifact path (cargo underscores dashes
// in the crate name) so the author never has to discover that rule.
func scaffoldRust(name, license string) error {
	if err := validName(name); err != nil {
		return err
	}
	expand := func(tmpl string) string {
		s := strings.ReplaceAll(tmpl, "NAME_US", strings.ReplaceAll(name, "-", "_"))
		s = strings.ReplaceAll(s, "NAME", name)
		return strings.ReplaceAll(s, "KITTAG", kitVersion())
	}
	return writeScaffold(name, map[string]string{
		"Cargo.toml": expand(tmplCargoToml),
		"src/lib.rs": expand(tmplLibRs),
		"README.md":  expand(tmplRustReadme),
		"smoke.yaml": tmplSmoke,
		"LICENSE":    license,
	})
}

const tmplMain = `// NAME — a shellcade game. Run it right now: go run .
package main

import (
	"fmt"
	"time"

	kit "github.com/shellcade/kit/v2"
)

func main() { kit.Main(Game{}) }

// Game is the registry entry: metadata + a per-room behavior factory.
type Game struct{}

func (Game) Meta() kit.GameMeta {
	return kit.GameMeta{
		Slug:             "NAME",
		Name:             "NAME",
		ShortDescription: "Describe your game in one line.",
		MinPlayers:       1,
		MaxPlayers:       4,
	}
}

func (Game) NewRoom(cfg kit.RoomConfig, svc kit.Services) kit.Handler {
	return &room{}
}

// room is one live room. ALL state lives here (and only here) — the host can
// snapshot and restore it, so key anything durable by Player.AccountID.
type room struct {
	kit.Base
	presses  int
	deadline time.Time // a wake-driven one-shot: see OnWake
}

func (rm *room) OnStart(r kit.Room) {
	r.SetInputContext(kit.CtxNav)
}

func (rm *room) OnJoin(r kit.Room, p kit.Player) { rm.render(r) }

func (rm *room) OnInput(r kit.Room, p kit.Player, in kit.Input) {
	switch kit.Resolve(in, kit.CtxNav) {
	case kit.ActConfirm:
		rm.presses++
		// One-shot timer, the wake way: store a deadline, check it in OnWake.
		rm.deadline = r.Now().Add(2 * time.Second)
	}
	rm.render(r)
}

// OnWake is the host heartbeat — the ONLY time your code runs without input.
// Drive every animation, countdown, and timeout from CallContext time here.
func (rm *room) OnWake(r kit.Room) {
	if !rm.deadline.IsZero() && r.Now().After(rm.deadline) {
		rm.deadline = time.Time{}
		rm.presses = 0 // the timeout fired: reset
	}
	rm.render(r)
}

func (rm *room) render(r kit.Room) {
	f := kit.NewFrame() // frames are POINTERS, always (see ABI.md §6)
	title := kit.Style{FG: kit.Cyan, Attr: kit.AttrBold}
	dim := kit.Style{FG: kit.DimGray}

	f.Text(2, 4, "*** NAME ***", title)
	f.Text(10, 4, fmt.Sprintf("SPACE pressed %d times", rm.presses), kit.Style{FG: kit.White})
	if !rm.deadline.IsZero() {
		left := rm.deadline.Sub(r.Now()).Round(100 * time.Millisecond)
		f.Text(12, 4, fmt.Sprintf("resetting in %s...", left), kit.Style{FG: kit.Yellow})
	}
	f.Text(kit.Rows-1, 2, "SPACE press   Esc leave", dim)

	for _, p := range r.Members() {
		r.Send(p, f)
	}
}
`

const tmplExports = `//go:build wasip1 || tinygo.wasm

package main

import kit "github.com/shellcade/kit/v2"

func init() { kit.Run(Game{}) }

// The eight shellcade ABI exports, trampolined to the SDK.

//go:export shellcade_abi
func expABI() int32 { return kit.ExportABI() }

//go:export meta
func expMeta() int32 { return kit.ExportMeta() }

//go:export start
func expStart() int32 { return kit.ExportStart() }

//go:export join
func expJoin() int32 { return kit.ExportJoin() }

//go:export leave
func expLeave() int32 { return kit.ExportLeave() }

//go:export input
func expInput() int32 { return kit.ExportInput() }

//go:export wake
func expWake() int32 { return kit.ExportWake() }

//go:export close
func expClose() int32 { return kit.ExportClose() }
`

// tmplSmoke is the scaffold's smoke.yaml — the deterministic scripted-screens
// contract every catalog game must ship (CI hard-fails without it). It passes
// against the template game as scaffolded (joining renders the opening
// screen; space resolves to Confirm and bumps the press counter), so it
// doubles as a working example of the schema.
const tmplSmoke = `# Scripted screens: catalog CI runs this on every PR and posts the named
# dumps as a visual preview. Run it locally with
#
#   shellcade-kit smoke .
#
# (Go authors can also run it natively: go run . -smoke smoke.yaml.)
# Schema + authoring guidance: kit GUIDE.md "Smoke scripts".
seed: 42
seats: 1
steps:
  - shot: opening
  - key: space # Confirm in the template game: bumps the press counter
  - shot: pressed
`

// fallbackKitVersion is used when build info lacks the dependency — only
// go.work/replace-style dev builds of this CLI; release binaries embed the
// real kit version. It MUST stay a valid /v2 module version (and existing
// kit release tag), or a dev-built CLI scaffolds an untidyable go.mod and an
// unresolvable cargo git pin.
const fallbackKitVersion = "v2.8.0"

// kitVersion is the kit module version this binary was BUILT AGAINST — the
// scaffold pins the same version, so templates can never drift.
func kitVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/shellcade/kit/v2" && d.Version != "" && d.Version != "(devel)" {
				return d.Version
			}
		}
	}
	return fallbackKitVersion
}

const tmplGoMod = `module NAME

go 1.25

require github.com/shellcade/kit/v2 KITVERSION
`

const tmplReadme = `# NAME — a shellcade game

## Develop (instant, no wasm)

    go run .                 # play in this terminal; Esc leaves
    go run . -seats 2        # hot-seat multiplayer; Ctrl-T switches seats
    go run . -seed 42        # reproducible runs

## Build the wasm artifact (~4s)

    tinygo build -opt=1 -no-debug -gc=conservative \
        -o NAME.wasm -target wasip1 -buildmode=c-shared .

Then verify with the shellcade developer kit: shellcade-kit check NAME.wasm — and play the
real artifact with shellcade-kit play NAME.wasm. (Both also accept the game
directory: shellcade-kit check . builds the artifact for you.)

## Submit

This directory is already catalog-shaped: LICENSE and smoke.yaml are required
by github.com/shellcade/games CI (see its SCHEMA.md), and smoke.yaml's screens
are posted on your PR as a visual preview. Preview them locally:

    shellcade-kit smoke .            # or natively: go run . -smoke smoke.yaml

## Learn more

- GUIDE.md in github.com/shellcade/kit/v2 — the authoring guide
- ABI.md — the contract your game targets
- github.com/shellcade/games — published example games using every SDK feature
`

// ---- Rust scaffold templates ------------------------------------------------

const tmplCargoToml = `[package]
name = "NAME"
version = "0.1.0"
edition = "2021"
publish = false

[lib]
# cdylib = the WASI reactor the arcade instantiates; rlib so cargo test links
# your game logic natively (no wasm runtime in the inner loop).
crate-type = ["cdylib", "rlib"]

[dependencies]
# Pinned to the kit release this shellcade-kit binary shipped with. Upgrade by
# bumping the tag to a newer kit release.
shellcade-kit = { git = "https://github.com/shellcade/kit", tag = "KITTAG" }

# The release profile IS the artifact contract: cargo applies profiles only at
# this (leaf) crate, never from a dependency. Without this block your artifact
# is a multi-megabyte debug build that fails size review.
[profile.release]
opt-level = "s"
lto = true
strip = true
panic = "abort"
`

const tmplLibRs = `// NAME — a shellcade game in Rust.
//
//   cargo test            # game logic, natively
//   shellcade-kit play .  # build the wasm artifact (wasm32-wasip1) + play it
//   shellcade-kit check . # the conformance harness, the catalog merge gate
//
// The built artifact is target/wasm32-wasip1/release/NAME_US.wasm.
#![forbid(unsafe_code)]

use shellcade_kit::prelude::*;

struct TheGame;

impl Game for TheGame {
    fn meta(&self) -> Meta {
        Meta {
            slug: "NAME", // == your catalog directory name
            name: "NAME",
            short_description: "Describe your game in one line.",
            min_players: 1,
            max_players: 4,
            ..Meta::DEFAULT
        }
    }
    fn new_room(&self, _cfg: &RoomConfig) -> Box<dyn Handler> {
        Box::new(TheRoom { frame: Frame::new(), presses: 0, deadline: None })
    }
}

// One live room. ALL state lives here (and only here) — the host can snapshot
// and restore it, so key anything durable by player.account_id.
struct TheRoom {
    frame: Frame,
    presses: u32,
    deadline: Option<i64>, // a wake-driven one-shot: see on_wake
}

impl Handler for TheRoom {
    fn on_start(&mut self, r: &mut Room) {
        r.set_input_context(InputContext::Nav);
    }

    fn on_join(&mut self, r: &mut Room, _p: Player) {
        self.render(r);
    }

    fn on_input(&mut self, r: &mut Room, _p: Player, input: Input) {
        if input.resolve(InputContext::Nav) == Action::Confirm {
            self.presses += 1;
            // One-shot timer, the wake way: store a deadline, check it in on_wake.
            self.deadline = Some(r.now_unix_nanos() + 2_000_000_000);
        }
        self.render(r);
    }

    // on_wake is the host heartbeat — the ONLY time your code runs without
    // input. Drive every animation, countdown, and timeout from room time here.
    fn on_wake(&mut self, r: &mut Room) {
        if let Some(d) = self.deadline {
            if r.now_unix_nanos() > d {
                self.deadline = None;
                self.presses = 0; // the timeout fired: reset
            }
        }
        self.render(r);
    }
}

impl TheRoom {
    fn render(&mut self, r: &mut Room) {
        let title = Style { fg: CYAN, attr: ATTR_BOLD, ..Style::default() };
        let dim = Style::new(DIM_GRAY, 0);

        let f = &mut self.frame;
        f.clear(); // reuse one frame: the allocation-free steady state
        f.text(2, 4, "*** NAME ***", title);
        f.text(10, 4, &format!("SPACE pressed {} times", self.presses), Style::new(WHITE, 0));
        if let Some(d) = self.deadline {
            let secs = (d - r.now_unix_nanos()) as f64 / 1e9;
            f.text(12, 4, &format!("resetting in {secs:.1}s..."), Style::new(YELLOW, 0));
        }
        f.text(ROWS - 1, 2, "SPACE press   Esc leave", dim);

        for p in r.members().to_vec() {
            r.send(&p, &self.frame);
        }
    }
}

shellcade_kit::shellcade_game!(TheGame);
`

const tmplRustReadme = `# NAME — a shellcade game (Rust)

## Develop

    rustup target add wasm32-wasip1   # once
    cargo test               # game logic runs natively (no wasm runtime)
    shellcade-kit play .     # build the wasm artifact AND play it, one command
    shellcade-kit smoke .    # build + run smoke.yaml, write the shot files

The see-it-on-screen loop is cargo test for logic plus a wasm build to play —
shellcade-kit play . does the build for you each iteration.

## Build + verify the artifact by hand

    cargo build --release --target wasm32-wasip1

The artifact is target/wasm32-wasip1/release/NAME_US.wasm (cargo converts
dashes in the crate name to underscores).

Then verify with the shellcade developer kit (check also accepts the
directory: shellcade-kit check .):

    shellcade-kit check target/wasm32-wasip1/release/NAME_US.wasm

## Submit

This directory is already catalog-shaped: LICENSE and smoke.yaml are required
by github.com/shellcade/games CI (see its SCHEMA.md), and smoke.yaml's screens
are posted on your PR as a visual preview.

## Learn more

- rust/README.md in github.com/shellcade/kit — the Rust quickstart + Go↔Rust dictionary
- GUIDE.md — the authoring guide (the mental model carries over one-for-one)
- ABI.md — the contract your game targets
- github.com/shellcade/games — published example games
`
