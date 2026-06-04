// Command kit is the author CLI for the shellcade game developer kit.
//
//	gamekit new <name>    scaffold a complete, playable game in ./<name>/
//
// The scaffold runs immediately: `go run .` plays it natively in your
// terminal; the TinyGo recipe in its README builds the wasm artifact.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "new" {
		fmt.Fprintln(os.Stderr, "usage: kit new <name>")
		os.Exit(2)
	}
	name := strings.ToLower(os.Args[2])
	if err := scaffold(name); err != nil {
		fmt.Fprintln(os.Stderr, "kit:", err)
		os.Exit(1)
	}
	fmt.Printf("Scaffolded %s/ — try it now:\n\n  cd %s && go mod tidy && go run .\n\nSee %s/README.md for the wasm build and next steps.\n", name, name, name)
}

func scaffold(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\ ") {
		return fmt.Errorf("name must be a simple directory name, got %q", name)
	}
	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("%s already exists", name)
	}
	files := map[string]string{
		"main.go":    strings.ReplaceAll(tmplMain, "NAME", name),
		"exports.go": tmplExports,
		"go.mod":     strings.ReplaceAll(tmplGoMod, "NAME", name),
		"README.md":  strings.ReplaceAll(tmplReadme, "NAME", name),
	}
	if err := os.MkdirAll(name, 0o755); err != nil {
		return err
	}
	for f, content := range files {
		if err := os.WriteFile(filepath.Join(name, f), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const tmplMain = `// NAME — a shellcade game. Run it right now: go run .
package main

import (
	"fmt"
	"time"

	kit "github.com/shellcade/kit"
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

import kit "github.com/shellcade/kit"

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

const tmplGoMod = `module NAME

go 1.25

require github.com/shellcade/kit v0.2.0
`

const tmplReadme = `# NAME — a shellcade game

## Develop (instant, no wasm)

    go run .                 # play in this terminal; Esc leaves
    go run . -seats 2        # hot-seat multiplayer; Ctrl-T switches seats
    go run . -seed 42        # reproducible runs

## Build the wasm artifact (~4s)

    tinygo build -opt=1 -no-debug -gc=leaking \
        -o NAME.wasm -target wasip1 -buildmode=c-shared .

Then verify with the shellcade developer kit: shellcade-kit check NAME.wasm — and play the
real artifact with shellcade-kit play NAME.wasm.

## Learn more

- GUIDE.md in github.com/shellcade/kit — the authoring guide
- ABI.md — the contract your game targets
- examples/pokies — a complete game using every SDK feature
`
