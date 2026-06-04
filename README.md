# gamekit — shellcade guest SDK (PROTOTYPE)

The authoring SDK for shellcade wasm games (ABI v1). Implemented purely from
the ABI contract; imports no shellcade private code. See
`openspec/changes/add-wasm-game-abi/` and `add-game-devkit/` for the design.

## Write a game

Implement `gamekit.Game` + `gamekit.Handler` (six callbacks: OnStart/OnJoin/
OnLeave/OnInput/OnWake/OnClose), call `gamekit.Run(game)` and add the eight
`//go:export` trampolines — see `examples/pokies/main.go`.

Rules of the road:
- **Frames are pointers** (`*gamekit.Frame`). A frame is ~46KB; by-value frames
  explode TinyGo compile time (3s → 3min) and artifact size (600KB → 9MB).
- **Time comes from `r.Now()`** (CallContext time) and code runs only when the
  host calls you — built-in timers/goroutines never fire. Drive animations and
  deadlines from `OnWake` (the host heartbeat).
- **Per-player durable state** via `r.Services().Accounts.For(p).Store()` —
  the kv keys are namespaced to your game and the player by the host.

## Build (dev profile — ~4 seconds)

    tinygo build -opt=1 -no-debug -gc=leaking -o game.wasm \
        -target wasip1 -buildmode=c-shared .

- `-opt=1` skips binaryen/wasm-opt (the slow part). Release builds can use
  `-opt=2`; expect minutes, run it in CI not your inner loop.
- `-opt=0` is NOT supported (giant unoptimized functions crash wazero's
  arm64 compiler).
- `-gc=leaking` is required for now: TinyGo 0.41's conservative GC faults in
  this reactor configuration (recorded finding; gamekit keeps the steady state
  allocation-free so the leak rate is negligible for play sessions).

## Test and play

    devkit check game.wasm     # ABI handshake, meta, scripted room
    devkit play  game.wasm     # play it in this terminal (Esc to leave)
    # flags: --seed N --heartbeat 50ms --config key=value --players N

From the shellcade repo: `make play-pokies` builds the example and plays it.
