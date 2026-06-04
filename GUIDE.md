# Writing a shellcade game

shellcade games are multiplayer terminal games on a fixed **80×24** canvas,
reachable over SSH at shellcade.com. You write a game against this SDK, test
it locally (no wasm, no network, no setup), compile it to WebAssembly, and
submit the artifact — the arcade hosts it sandboxed. You never need
shellcade's source; this module and [ABI.md](ABI.md) are the whole contract.

## Zero to playable in two minutes

```sh
go run github.com/shellcade/kit/cmd/kit@latest new mygame
cd mygame && go mod tidy && go run .
```

You're playing your game. Edit `main.go`, `go run .` again — the inner loop is
a normal Go program (debugger, prints, sub-second builds). The wasm toolchain
only enters when you want the real artifact.

## The mental model

A game is **a function from events to frames**. You implement six callbacks;
the arcade calls them one at a time (never concurrently) and you respond by
composing 80×24 frames:

```go
type Handler interface {
    OnStart(r Room)                          // the room exists
    OnJoin(r Room, p Player)                 // someone sat down
    OnLeave(r Room, p Player)                // someone left
    OnInput(r Room, p Player, in Input)      // someone pressed a key
    OnWake(r Room)                           // the heartbeat (see below)
    OnClose(r Room)                          // the room is over
}
```

Three rules shape everything:

1. **Your code only runs inside callbacks.** There are no goroutines-between-
   events, no `time.Sleep`, no `setTimeout`-equivalents — built-in timers
   simply never fire. `OnWake`, called ~20×/second while players are
   connected, is the only way your game advances without input.
2. **All state lives in your room struct.** There's nowhere else. The arcade
   can freeze an idle room (snapshot) and revive it later — even across
   deploys — and your game won't notice. One consequence: key durable
   per-player state by `Player.AccountID`, never by `Player.Conn` (connections
   change across a freeze; accounts don't).
3. **Render on change, frames by pointer.** Compose a `*Frame` and `Send` it
   whenever your state changed — from any callback. Never pass `Frame` by
   value (it's ~46KB; see ABI.md §6 for why this matters a lot under TinyGo).

## Time, the wake way

Everything the standard library's timers would do becomes a comparison
against `r.Now()` inside `OnWake`. The three idioms (all live in
[examples/pokies](examples/pokies)):

```go
// One-shot (was: time.AfterFunc) — store a deadline, check it on wake.
rm.flashUntil = r.Now().Add(1500 * time.Millisecond)
// in OnWake:
if rm.flash != "" && r.Now().After(rm.flashUntil) { rm.flash = "" }

// Animation clock (was: time.Ticker) — DERIVE the position from elapsed time.
// Never accumulate per-wake; derived clocks are framerate-independent and
// survive hibernation.
cycle := int(r.Now().Sub(rm.spinStart) / (80 * time.Millisecond))

// Staggered schedule — precompute the deadlines, land what's due.
due := rm.spinStart.Add(150*time.Millisecond + time.Duration(i)*250*time.Millisecond)
if r.Now().After(due) { rm.landReel(i) }
```

`r.Now()` is the room clock: consistent with the host, deterministic under
`-seed`, and the same value your language's `time.Now()` returns (the arcade
virtualizes the clock — accidental use is harmless, by design).

## Players, input, and controls

`OnInput` delivers raw keys; resolve them through the platform's canonical
control vocabulary so your game matches every other game's conventions
(↑/k, ↓/j, Enter/Space confirm, Esc/q back):

```go
switch kit.Resolve(in, kit.CtxNav) {
case kit.ActUp:      // ...
case kit.ActConfirm: // ...
}
```

Declare your input context once (`r.SetInputContext`): `CtxNav` for menus and
move-around games, `CtxCommand` when letters are commands (blackjack's
h/s/d), `CtxText` for typing games.

## Durable state: the per-player KV

```go
store := r.Services().Accounts.For(p).Store()
store.Set(ctx, "balance", []byte("990"), kit.MergeSum)
v, ok, _ := store.Get(ctx, "balance")
```

Keys are namespaced to *your game* and *that player* by the host — you cannot
read another game's data, and nobody can read yours. The merge rule says how
a key reconciles if two accounts merge: `sum` for currencies, `max` for high
scores, `keep-winner` (default) for everything else.

Per-game configuration (tunable by arcade admins without your involvement)
arrives through `r.Services().Config.Get(ctx, "key")` — read it on a slow
cadence in `OnWake` and fall back to compiled defaults when absent.

## Multiplayer

Rooms hold 1–N players (your `GameMeta` declares the range). Render
**per-player views** by composing a frame per member:

```go
for _, p := range r.Members() {
    r.Send(p, rm.composeFor(p))   // each player sees their own view
}
```

Test it without leaving your terminal: `go run . -seats 3` joins three
players; **Ctrl-T** switches which seat your keyboard controls, and the view
follows — so you can play every side of your own game.

## The full loop

| Stage | Command | What it proves |
|---|---|---|
| iterate | `go run .` (~0.1s) | gameplay, rendering, logic |
| artifact | `tinygo build -opt=1 -no-debug -gc=leaking -target wasip1 -buildmode=c-shared .` (~4s) | the real wasm builds |
| verify | `shellcade-kit check game.wasm` | ABI conformance, budgets, determinism — the same gate the arcade runs |
| play it | `shellcade-kit play game.wasm --seats 2` | the artifact, on the production engine |

`shellcade-kit check` passing locally **is** the arcade's acceptance bar — same
code, same verdict.

## What your game can't do (on purpose)

No filesystem, no network, no system clock, no threads: the sandbox gives
your game exactly the eleven host functions in ABI.md §3 and nothing else.
Resource budgets (memory cap, per-callback deadline) are enforced and
reported by `shellcade-kit check` — stay comfortably inside them and an arcade full
of other games stays healthy around yours.

## Study a complete game

[examples/pokies](examples/pokies) is the reference: a 1–5 player slot
machine exercising every feature — wake-driven reel animation, staggered
one-shots, admin-tunable odds via config, a durable wallet with merge rules,
and per-player rendering. It's a faithful port of the arcade's own native
pokies, so it reads like production code, because it is.
