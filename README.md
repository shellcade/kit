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
| `rust/` | the [Rust guest SDK](rust/README.md) (`shellcade-kit new --rust`) |
| `crossverify/` | golden vectors binding the Rust delta encoder byte-identical to Go's |
| `ABI.md` / `GUIDE.md` | the normative contract / the authoring guide |

For complete, published example games, see the
[games catalog](https://github.com/shellcade/games) — every game there is
conformance-green; [pokies](https://github.com/shellcade/games/tree/main/games/bcook/pokies)
exercises every SDK feature.

## Write a game

Implement `kit.Game` + `kit.Handler` (six callbacks: OnStart/OnJoin/
OnLeave/OnInput/OnWake/OnClose), call `kit.Run(game)` and add the eight
`//go:export` trampolines. The fastest start is `shellcade-kit new mygame`,
which scaffolds exactly this; for a full real-game reference see
[pokies](https://github.com/shellcade/games/tree/main/games/bcook/pokies) in
the catalog.

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

       tinygo build -opt=1 -no-debug -gc=conservative -o game.wasm \
           -target wasip1 -buildmode=c-shared .
3. **Release:** `-opt=2` in CI (minutes — never in your inner loop).

- `-opt=1` skips binaryen/wasm-opt (the slow part). Release builds can use
  `-opt=2`; expect minutes, run it in CI not your inner loop.
- `-opt=0` is NOT supported (giant unoptimized functions crash wazero's
  arm64 compiler).
- `-gc=conservative` is the build profile (since 2026-06-11): leaking GC made
  every allocation permanent, so long-lived rooms hit the host's 32 MiB cap
  and trapped (~52 min of play in production). The previously recorded
  TinyGo-0.41 conservative-GC fault does not reproduce on 0.41.1
  (200k-callback soak, flat memory). Keep steady-state paths allocation-free
  anyway — it minimizes GC pauses inside the callback deadline.

## Test and play

    shellcade-kit check game.wasm     # ABI handshake, meta, scripted room
    shellcade-kit check .             # + lint Go source for wide-glyph width-contract bugs, then build & check
    shellcade-kit lint-width .        # that source lint on its own (file/dir paths; no build)
    shellcade-kit play  game.wasm     # play it in this terminal (Esc to leave)
    # flags: --seed N --heartbeat 50ms --config key=value --seats N

**Multiplayer testing is hot-seat — no SSH, no network.** Pass `--seats N`
(or `-seats N` to `go run .`) to join N players to the one room; your
keyboard drives the active seat and **Ctrl-T** switches seats, so you can play
both sides of a duel from one terminal. The wasm runner renders each seat's own
per-player frame, so seat-switching also verifies per-viewer composition.

To play a real published game's artifact, clone the
[games catalog](https://github.com/shellcade/games), build a game's wasm with
the dev-profile `tinygo build` above, and `shellcade-kit play game.wasm`.
