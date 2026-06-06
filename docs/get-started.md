# Get started with shellcade

A shellcade game is a small WebAssembly plugin that draws to a fixed
80x24 terminal canvas. You write it in Go (or Rust) against this kit,
test it locally with no wasm and no network, compile it to wasm, and
submit the artifact — the arcade hosts it sandboxed and reachable over
SSH at shellcade.com.

## Quickstart

Grab the one author tool, `shellcade-kit` (scaffold, verify, play), from
this repo's Releases:

    https://github.com/shellcade/kit/releases

(macOS may quarantine the download:
`xattr -d com.apple.quarantine shellcade-kit` clears it.)

Scaffold a game and play it in your terminal — the inner loop is a plain
Go program (debugger, prints, sub-second builds), no wasm required:

    shellcade-kit new mygame
    cd mygame && go mod tidy && go run .

Edit `main.go`, run `go run .` again, and you are iterating. When you
want the real artifact, build the wasm and check it against the same
gate the arcade runs, then play that artifact on the production engine:

    shellcade-kit check game.wasm
    shellcade-kit play game.wasm

`check` passing locally is the arcade's acceptance bar — same code, same
verdict. Multiplayer testing is hot-seat: pass `--seats N` to join N
players to one room and Ctrl-T to switch the seat your keyboard drives.

Prefer Rust? `shellcade-kit new --rust mygame` scaffolds the same game
shape on the Rust SDK; the mental model carries over one for one.

## Publish your game

Games live in the public catalog at github.com/shellcade/games. To ship:

1. Build your wasm and confirm `shellcade-kit check game.wasm` is green.
2. Open a pull request that adds your game to the catalog.
3. When the PR is merged it cuts a release of your game.
4. An operator reviews the release and takes it live in the arcade.

Every game in the catalog is conformance-green, so the check gate is the
bar both for your PR and for going live. Read a complete, published game
to learn the patterns before you submit.

## Link your GitHub

Link your GitHub account so the games you publish are attributed to you
in the arcade. Connect over SSH:

    ssh shellcade.com

Then open the User menu and choose Link GitHub, and follow the prompt.
Once linked, games you author show up under your name.

## Where to go next

- GUIDE.md — the full authoring guide: the event-to-frame model, time
  via OnWake, input and controls, leaderboards, durable per-player KV,
  multiplayer rendering, and smoke scripts.
- ABI.md — the normative contract: the wasm ABI, host functions, and
  the packed payload encodings the kit compiles against.
- github.com/shellcade/games — the public catalog of published games to
  read and learn from.
