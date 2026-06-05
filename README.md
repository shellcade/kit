# kit — the shellcade game developer kit

Write multiplayer terminal games for [shellcade.com](https://shellcade.com) —
develop and test locally with zero setup, compile to WebAssembly, submit the
artifact. This module is the complete contract: the `wire` package and
[ABI.md](ABI.md) define the ABI, and no shellcade-private code is needed
(or referenced) anywhere here.

**Start with [GUIDE.md](GUIDE.md)** — or go straight to playing: grab
**`shellcade-kit`** from this repo's [Releases](https://github.com/shellcade/kit/releases)
(the one author tool: scaffold, verify, and play artifacts), then:

```sh
shellcade-kit new mygame
cd mygame && go mod tidy && go run .
```

## Layout

| Path | What |
|---|---|
| `kit` (root) | the authoring surface: `Game`/`Handler`/`Room`, frames, controls |
| `keyhold/`, `kittest/` | held-keys helper for action games; in-memory test double |
| `wire/` | the ABI as code: version, names, packed payload codecs |
| `examples/pokies` | the reference game (uses every SDK feature) |
| `ABI.md` / `GUIDE.md` | the normative contract / the authoring guide |

## Write a game

Implement `kit.Game` + `kit.Handler` (six callbacks: OnStart/OnJoin/
OnLeave/OnInput/OnWake/OnClose), call `kit.Run(game)` and add the eight
`//go:export` trampolines — see `examples/pokies/main.go`.

Rules of the road:
- **Frames are pointers** (`*kit.Frame`). A frame is ~46KB; by-value frames
  explode TinyGo compile time (3s → 3min) and artifact size (600KB → 9MB).
- **Time comes from `r.Now()`** (CallContext time) and code runs only when the
  host calls you — built-in timers/goroutines never fire. Drive animations and
  deadlines from `OnWake` (the host heartbeat).
- **Per-player durable state** via `r.Services().Accounts.For(p).Store()` —
  the kv keys are namespaced to your game and the player by the host.

## The dev loop (three gears)

1. **Inner loop — no wasm at all (~0.1s):** `go run .` in your game directory
   plays the game natively in your terminal via `kit.Main` — normal Go
   builds, delve, prints, real stack traces.
   Flags: `-seed N -heartbeat 50ms -config k=v -handle name`.
2. **Artifact check (~4s):** build the real wasm and verify it:

       tinygo build -opt=1 -no-debug -gc=leaking -o game.wasm \
           -target wasip1 -buildmode=c-shared .
3. **Release:** `-opt=2` in CI (minutes — never in your inner loop).

- `-opt=1` skips binaryen/wasm-opt (the slow part). Release builds can use
  `-opt=2`; expect minutes, run it in CI not your inner loop.
- `-opt=0` is NOT supported (giant unoptimized functions crash wazero's
  arm64 compiler).
- `-gc=leaking` is required for now: TinyGo 0.41's conservative GC faults in
  this reactor configuration (recorded finding; kit keeps the steady state
  allocation-free so the leak rate is negligible for play sessions).

## Test and play

    shellcade-kit check game.wasm     # ABI handshake, meta, scripted room
    devkit play  game.wasm     # play it in this terminal (Esc to leave)
    # flags: --seed N --heartbeat 50ms --config key=value --seats N

**Multiplayer testing is hot-seat — no SSH, no network.** Pass `--seats N`
(or `-seats N` to `go run .`) to join N players to the one room; your
keyboard drives the active seat and **Ctrl-T** switches seats, so you can play
both sides of a duel from one terminal. The wasm runner renders each seat's own
per-player frame, so seat-switching also verifies per-viewer composition.

From the shellcade repo: `make play-pokies` builds the example and plays it.
