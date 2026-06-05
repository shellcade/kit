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
3. **Render on change, frames by pointer, reuse the buffer.** Compose a
   `*Frame` and `Send` it whenever your state changed — from any callback.
   Never pass `Frame` by value (~46KB; ABI.md §6), and prefer ONE long-lived
   frame over `NewFrame()` per render — `Send` copies immediately, so the
   allocation-free idiom is:

   ```go
   type room struct{ frame *kit.Frame /* ... */ }   // allocate once
   // per render, per player:
   rm.frame.Clear()
   rm.drawFor(p, rm.frame)
   r.Send(p, rm.frame)
   ```

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

`r.Now()` is the room clock: consistent with the host, and the same value your
language's `time.Now()` returns (the arcade virtualizes the clock — accidental
use is harmless, by design).

> **Determinism caveat:** in the native `go run .` runner, `r.Now()` is the
> real wall clock, so time-derived behavior is NOT reproducible under `-seed`
> there. The wasm path (`shellcade-kit check`) is the determinism source of
> record, and the `kittest` package gives you a virtual clock in unit tests.

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

## Action games: held keys, raw input, geometry

Terminals (and SSH) deliver only discrete key events — **there is no key-up**,
and "holding" a key produces press → pause → auto-repeat at the *user's*
terminal rate. For hold-to-thrust/fire semantics, use the `keyhold` package,
which derives held state from repeat timing:

```go
import "github.com/shellcade/kit/keyhold"

rm.keys = keyhold.New(0)                          // in NewRoom
rm.keys.Observe(in, r.Now())                      // in OnInput
if rm.keys.Held(kit.KeyUp, r.Now()) { ... }       // in OnWake
```

Integrate physics against **elapsed time while held** — never per input event
(event rate is the user's repeat rate). The trade-off is release latency (a key
reads held for up to the linger after letting go); tune `keyhold.New(linger)`
for feel.

More action-game realities:

- **`Resolve` is menu-shaped** (Up/Down/Confirm/Back). For a shooter, read the
  raw `Input` directly and keep `Resolve` for your menus.
- **Cells are ~2:1 tall.** Circular motion and round collisions need an aspect
  correction (multiply y-velocity/area by ~0.5) or everything looks ovoid.
- **Reserved keys:** the local runners reserve `Esc`/`Ctrl-C` (leave) and
  `Ctrl-T` (seat switch); in the arcade, `Esc` is the lobby's Back. Don't bind
  gameplay to them.

## Scores and leaderboards

`GameMeta.Leaderboard` *declares* the board (label, direction, aggregation,
format); your game *feeds* it with `Post` or `End` — **never via KV** (KV is
durable state; the board doesn't read it):

- **`End(result)`** settles the room **once** — for round-based games. Every
  player's `PlayerResult{Metric, Rank, Status}` is recorded and the room ends.
- **`Post(result)`** records results **without ending the room** — for endless
  or social games. Post when a score is *banked* (a new personal best, a
  finished run, a kill streak cashed in), not every frame; the board keeps each
  account's best (or sum, per your `Aggregation`).

Worked example (endless arena, best-kills board):

```go
Leaderboard: &kit.LeaderboardSpec{MetricLabel: "Kills", Direction: kit.HigherBetter}

// on death/respawn — bank the run:
r.Post(kit.Result{Rankings: []kit.PlayerResult{{
    Player: p, Metric: run.kills, Status: kit.StatusFinished,
}}})
```

See `examples/pokies`: it posts a player's new `peak` after a winning spin —
the KV write keeps the wallet durable; the `Post` is what reaches the board.

## Durable state: the per-player KV

```go
store := r.Services().Accounts.For(p).Store()
store.Set(ctx, "balance", []byte("990"), kit.MergeSum)
v, ok, _ := store.Get(ctx, "balance")
```

Keys are namespaced to *your game* and *that player* by the host — you cannot
read another game's data, and nobody can read yours. The merge rule says how
a key reconciles if two accounts merge: `sum` for currencies, `max` for high
scores, `keep-winner` (default) for everything else. **`sum`/`max` values MUST
be base-10 ASCII integers** (e.g. `strconv.Itoa` — `"990"`), within int64;
anything unparsable falls back to keep-winner at merge time.

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

> **TinyGo is the artifact toolchain.** `GOOS=wasip1 go build ./...` with the
> standard toolchain type-checks your wasm-tagged files (useful!) but does NOT
> produce a valid artifact — `//go:export` is TinyGo's directive; a stdlib
> build won't export the trampolines.

## The whole Room, at a glance

| Member | What | Notes |
|---|---|---|
| `Members/Has/Count` | roster reads | free (local) |
| `Config` | mode, capacity, seed | free (local) |
| `Rand` | seeded PRNG | deterministic under `-seed` |
| `Now` | room clock | wall-clock in the native runner (see caveat) |
| `Settled` | has the room ended? | |
| `Send(p, *Frame)` | one player's view | copies immediately — reuse the frame |
| `Identical(*Frame)` | same view to everyone | |
| `SetInputContext` | Nav / Command / Text | governs Back/q resolution |
| `End(Result)` | settle once and finish | round-based games |
| `Post(Result)` | record scores, keep playing | endless/social games |
| `Log(msg)` | host-side room log | visible in `shellcade-kit play` stderr |
| `Services()` | KV / config / accounts | see Durable state |

For unit tests, `github.com/shellcade/kit/kittest` is an in-memory `Room` +
`Services` with a virtual clock, seeded RNG, and recorded
frames/posts/settles — drive your `Handler` directly and assert.

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
