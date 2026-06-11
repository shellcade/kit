# Writing a shellcade game

shellcade games are multiplayer terminal games on a fixed **80×24** canvas,
reachable over SSH at shellcade.com. You write a game against this SDK, test
it locally (no wasm, no network, no setup), compile it to WebAssembly, and
submit the artifact — the arcade hosts it sandboxed. You never need
shellcade's source; this module and [ABI.md](ABI.md) are the whole contract.

## Zero to playable in two minutes

Grab **`shellcade-kit`** — the one author tool (scaffold, verify, play
artifacts) — from this repo's
[Releases](https://github.com/shellcade/kit/releases), then:

```sh
shellcade-kit new mygame
cd mygame && go mod tidy && go run .
```

(macOS may quarantine the downloaded binary:
`xattr -d com.apple.quarantine shellcade-kit` clears it.)

You're playing your game. Edit `main.go`, `go run .` again — the inner loop is
a normal Go program (debugger, prints, sub-second builds). The wasm toolchain
only enters when you want the real artifact.

Prefer Rust? `shellcade-kit new --rust mygame` scaffolds the same game shape
on the [Rust SDK](rust/README.md) — this guide's mental model carries over
one-for-one (the Rust README has the Go↔Rust dictionary).

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

## Cookbook: time, the wake way

The sandbox has **no timers, no goroutines, no `time.Sleep`** — your code runs
only inside callbacks, and the only one that fires without player input is
`OnWake` (the host heartbeat, ~20×/second while anyone is connected). So every
"later…" becomes a **comparison against `r.Now()` inside `OnWake`**.

`r.Now()` is the room clock the host owns: monotonic-enough for game logic, the
same value your language's `time.Now()` would return (the arcade virtualizes
the clock, so accidental use is harmless), and — under `go run . -seed N` — a
deterministic virtual clock that advances one heartbeat per wake. Build your
time logic against `r.Now()` and it behaves identically native, in wasm, and
under test.

The four idioms below are everything. They are drawn from the **pokies** game
in the [public catalog](https://github.com/shellcade/games/tree/main/games/bcook/pokies),
a 1–5 player slot machine that exercises every one of them in production code —
read [its `room.go`](https://github.com/shellcade/games/blob/main/games/bcook/pokies/room.go)
for the full, working versions. (The snippets bind `now := r.Now()` once at the
top of `OnWake`; that's the only clock read you ever need.)

### 1. One-shot deadline — porting `time.AfterFunc`

**Want:** do something once, T later. **Don't:** start a timer. **Do:** store
the deadline now; check it every wake; act when `Now()` passes it.

```go
// set the deadline when the event happens:
rm.flashUntil = r.Now().Add(flashDur)

// expire it on the heartbeat, in OnWake:
if rm.flash != "" && now.After(rm.flashUntil) {
    rm.flash = ""
}
```

The state (`flashUntil`) lives in your room struct, so it survives a
hibernation freeze/thaw — a real `AfterFunc` would not. This is the whole
pattern; "flash a banner for 1.5s" is the same shape as "expire a power-up".

### 2. Staggered schedule — many one-shots, precomputed

**Want:** several things to land at offset times (reel 0 at +150ms, reel 1 at
+400ms, …). **Do:** derive each deadline from one start time and an index;
land whatever is due this wake.

```go
// in OnWake: land every reel whose deadline has passed.
for i := rm.spin.landed; i < 3; i++ {
    due := rm.spin.startedAt.Add(reelStopBase + time.Duration(i)*reelStopStep)
    if !now.After(due) {
        break // not due yet, and later reels are even later — stop
    }
    rm.landReel(r, i)
}
```

Because deadlines are **derived from `startedAt`**, a slow frame, a paused tab,
or a hibernation gap can never desync them: when wakes resume, every overdue
reel lands in order on the next heartbeat. Never advance an index "per wake" —
that couples your animation to the (variable) heartbeat rate.

### 3. Animation clock — porting `time.Ticker`, derived not accumulated

**Want:** a smooth, repeating cadence (the reels scrolling while they spin).
**Do:** compute the current cycle from **elapsed time**, every render. Never
keep a counter you `++` each wake.

```go
// which scroll frame are we on right now?
func (s *spinState) cycle(now time.Time) int {
    return int(now.Sub(s.startedAt) / cycleRate)
}
```

A derived clock is framerate-independent (looks right whether the heartbeat is
20Hz or 5Hz), reproducible under `-seed`, and hibernation-stable. The same
trick drives a repeating cadence: "fire every N" is `now.Sub(start) / N`
changing value — or, for a periodic config refresh, a rolling next-deadline:

```go
// in OnWake: re-read admin config on a slow cadence.
if now.After(rm.nextCfg) {
    rm.loadVariant(r)
    rm.nextCfg = now.Add(configRefresh)
}
```

### 4. Turn / countdown timeout — the deadline pattern, applied to a turn

A turn timer is just idiom #1 with a player-facing countdown. Store the
deadline when the turn begins; show `deadline.Sub(now)` in your frame; on the
wake where `now.After(deadline)`, end the turn (auto-fold, skip, time-out).

```go
// armed when the turn (or, in pokies, a big-win banner) begins:
rm.ticker = ticker{text: msg, until: r.Now().Add(tickerDur)}

// read while live, in your renderer:
func (rm *room) tickerActive(now time.Time) bool {
    return rm.ticker.text != "" && now.Before(rm.ticker.until)
}
```

For a real turn timeout, swap "clear the banner" for "advance the turn" in the
`now.After(deadline)` branch of `OnWake`.

### "How do I port a timer?" — quick reference

| You had | You write |
|---|---|
| `time.AfterFunc(d, fn)` | store `deadline = Now().Add(d)`; in `OnWake`, `if Now().After(deadline) { fn() }` (idiom 1) |
| `time.NewTimer` you `Reset` | same, and reassign `deadline` when you'd `Reset` |
| `time.NewTicker(d)` | derive: `cycle := Now().Sub(start) / d` — read it, don't accumulate (idiom 3) |
| `time.Sleep(d)` then act | there is no sleep; it's a one-shot deadline (idiom 1) |
| N staggered callbacks | precompute each `deadline = start + offset(i)`; land what's due (idiom 2) |
| a countdown a player sees | idiom 1 + render `deadline.Sub(Now())` each frame (idiom 4) |
| `context.WithTimeout` for a turn | a turn deadline checked in `OnWake` (idiom 4) |

Three traps the idioms avoid:

1. **Don't accumulate.** `cycle++` each wake drifts with heartbeat rate and
   breaks across hibernation. Derive from elapsed time instead.
2. **Keep all time-state in the room struct.** It's the only thing that
   survives a freeze; a host-side timer wouldn't exist after a thaw.
3. **`OnWake` does the work, then renders once.** Advance every due deadline,
   then `Send` the frame: expire flashes, land reels, refresh config, and call
   your `render` last.

> **Native clock:** pass `-seed` and `r.Now()` becomes a **virtual** clock —
> it starts at a fixed epoch derived from the seed and advances by exactly one
> heartbeat per `OnWake` (and at no other moment), so a `-seed` run is
> reproducible: same seed, same frames. Without `-seed`, `r.Now()` is the real
> wall clock (handy for feeling out timing live). The wasm path
> (`shellcade-kit check`) is still the determinism source of record, and the
> `kittest` package gives you a virtual clock in unit tests.

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
import "github.com/shellcade/kit/v2/keyhold"

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
- **Double-width glyphs (CJK, many emoji).** `Frame.Text` is one-rune-one-cell,
  so a wide rune would visually overrun its neighbour. Use `Frame.SetWide(row,
  col, r, st)` instead: it writes the glyph plus a marked continuation cell so
  the rune owns both columns. It returns the next free col (`col+2`), and
  refuses (draws nothing) when the pair can't fit — out of bounds or at the
  right edge (`col == Cols-1`) — matching `Set`'s drop-on-overflow rule.
- **Reserved keys:** the local runners reserve `Esc`/`Ctrl-C` (leave) and
  `Ctrl-T` (seat switch); in the arcade, `Esc` is the lobby's Back. Don't bind
  gameplay to them.

## Grapheme glyphs (emoji with modifiers)

Some emoji are a single visible glyph built from **several** Unicode code
points: a heart plus a VS16 variation selector (`❤️` = `❤` + U+FE0F), a thumbs-up
plus a skin-tone modifier (`👍🏽`), a keycap (`1️⃣` = `1` + U+FE0F + U+20E3). A cell
holds **up to three** code points for exactly this, and you write one with
`SetGrapheme`:

```go
f.SetGrapheme(row, col, "❤️", st)   // heart + VS16, one cell, width 1
f.SetGrapheme(row, col, "1️⃣", st)   // keycap: '1' + VS16 + U+20E3
w := f.SetGraphemeWide(row, col, "👍🏽", st) // a width-2 grapheme; w == col+2
```

- `SetGrapheme(row, col, cluster, st) int` writes the cluster into one cell
  (base → first code point, then the extras) and returns the next column
  (`col+1`). `SetGraphemeWide(...)` is the width-2 companion — it marks a
  continuation cell at `col+1` and returns `col+2`, mirroring `SetWide`, and
  refuses (draws nothing) at the right edge rather than drawing a half-glyph.
- **Three code points is the ceiling.** A cluster that decodes to **more than
  three** code points — a family ZWJ emoji like `👨‍👩‍👧` (man + ZWJ + woman + ZWJ
  + girl, five code points) — is **unsupported**. `SetGrapheme` **refuses** it:
  it draws nothing and returns `col` unchanged. Refusal (not truncation) is
  deliberate — truncating to three code points would render a *different,
  valid-looking* glyph (a lone person instead of a family), a worse surprise
  than a blank.
- **Width is your contract, and your risk.** The SDK never measures display
  width — it does not pull a Unicode width table into your wasm artifact.
  Declaring width via `SetGraphemeWide` is the same author's-contract /
  author's-risk deal as `SetWide` today: **viewer terminals disagree** on how
  wide an emoji sequence (ZWJ, VS16, skin-tone, keycap) actually renders, so
  test on the terminals you care about and lay out defensively. A wrong width
  guess shifts everything to its right on terminals that disagree.
- **Ordinary text is unchanged.** `SetRune`, `Set`, `Text`, and `SetWide` are
  exactly as before — single-code-point writers that leave the extra slots
  empty. You only reach for `SetGrapheme` when you specifically want a
  multi-code-point emoji.

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

See [pokies](https://github.com/shellcade/games/tree/main/games/bcook/pokies)
in the catalog: it posts a player's new `peak` after a winning spin — the KV
write keeps the wallet durable; the `Post` is what reaches the board.

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

**The store can degrade, and you won't see an error.** The ABI gives your game
no error channel for KV: when the host's store has a transient failure, `Get`
reports the key as **missing** (`nil, false, nil`) and `Set`/`Delete` return
`nil` without persisting. That makes the natural
`Get → missing → initialize starting balance → Set` wallet pattern a trap — a
store blip reads a veteran's wallet as absent, and your "new player" write can
clobber the durable value. Treat a missing read conservatively (defer the
initializing write, or use `sum`/`max` rules so a blip-era write cannot win at
merge time), and test the scenario with `kittest`: set
`r.KVUnavailable = true` and your suite sees exactly those production
semantics (see `ExampleRoom_kvUnavailable` in the `kittest` package).

Per-game configuration (tunable by arcade admins without your involvement)
arrives through `r.Services().Config.Get(ctx, "key")` — read it on a slow
cadence in `OnWake` and fall back to compiled defaults when absent.

Declare the keys you read in `GameMeta.Config` so the arcade's admin tools can
render a real editor for them instead of a blind key/value prompt:

```go
Config: []kit.ConfigKeySpec{{
    Key:         "odds-variant",
    Title:       "Odds variant",
    Description: "PAR sheet: per-symbol reel weights plus the paytable.",
    Type:        kit.ConfigJSON,        // or ConfigText / ConfigNumber / ConfigBool
    Default:     defaultVariantJSON,    // what you use when the key is unset
    Schema:      variantSchemaJSON,     // optional JSON Schema → rich form editing
}},
```

Declarations are optional and validated at `meta()` time: keys must be unique,
non-empty, and never under the reserved `host.` prefix, and `Schema` (json keys
only) must parse as JSON. Admins can still set undeclared keys — specs improve
the editor, they don't gate the store.

## Multiplayer

Rooms hold 1–N players: your `GameMeta` declares the range, and the platform
bound is **1..1024** (`wire.RosterCap` — the same constant that sizes the
frame-delta roster on both sides of the ABI). Render **per-player views** by
composing a frame per member:

```go
for _, p := range r.Members() {
    r.Send(p, rm.composeFor(p))   // each player sees their own view
}
```

Test it without leaving your terminal: `go run . -seats 3` joins three
players; **Ctrl-T** switches which seat your keyboard controls, and the view
follows — so you can play every side of your own game.

The native runner also rides terminal resizes: shrink the window below 80×24
and it shows a "terminal too small" notice, then repaints your game the moment
you grow it back — the same letterboxing the arcade does over SSH.

## Choosing a lifecycle

`GameMeta.Lifecycle` declares what happens to your room when everyone
leaves:

- **`LifecycleResumable`** (the default): the room hibernates and players
  can resume it later from the lobby. Right for games where an interrupted
  match is worth returning to — chess, anything with long-arc state.
- **`LifecycleEphemeral`**: after the abandonment grace the room ends and
  disposes — no snapshot, no resume entry. Right for casual social rooms
  (slots, card tables, quick board games) where a match without its players
  is meaningless. The grace still protects against connection blips: a
  rejoin within it finds the room intact.
- **`LifecycleResident`**: one long-lived room per slug — the persistent-
  world shape. It keeps ticking with zero players, checkpoints
  periodically, and survives deploys without anyone resuming it. Declaring
  it is a REQUEST: the platform grants residency per slug (always-on
  compute is an operator decision), and an ungranted declaration simply
  behaves as resumable. Resident games must tolerate `r.Count() == 0` in
  every callback (all games should — `OnStart` fires before the first
  join), should idle-throttle expensive work when nobody is online, and
  cannot declare `MinPlayers > 1`. Ending a resident room (`r.End`) is the
  world-reset primitive: the next join creates a fresh world.

## Large rooms: 100+ players in one room

The SDK supports rooms of up to 1024 players (`wire.RosterCap`, the platform's
hard ceiling), but a large room only stays inside the wake budget if the game
follows three disciplines:

**Declare your heartbeat.** A roguelike or board game does not need the 50ms
default. Declare your real cadence and the platform honors it (an admin
override always wins):

```go
func (Game) Meta() kit.GameMeta {
    return kit.GameMeta{
        // ...
        HeartbeatMS: 100, // gentle tick: 10 wakes/sec
    }
}
```

**Declare the roster-epoch feature.** By default every callback payload
carries the full member list. In a 1000-player room that is ~100KB per
input; with the feature, an unchanged roster costs 6 bytes:

```go
CtxFeatures: kit.CtxFeatRosterEpoch,
```

The SDK handles the rest. One contract to know: the slice from
`r.Members()` is valid for the duration of the callback — copy any `Player`
you keep, and key long-lived state by `AccountID` (you already should).

**Render on change, not on wake.** Composing and sending every player's
frame on every wake is the single largest cost in a big room — and most
frames are identical to the last. Track per-player dirtiness and skip clean
viewports:

- re-compose a player's view only when THEY moved, something moved within
  their visible window, or their HUD line changed;
- throttle ambient HUD clocks to ~1Hz — a per-wake counter in the HUD forces
  a nonzero delta for every player on every wake, defeating the delta
  encoder;
- at typical input rates expect only ~10–30% of viewports dirty per tick —
  a 3–10× cut in wake cost.

One trap: mark a player dirty on REJOIN (same account re-seating, including
after a hibernation resume) — their frame baselines were invalidated, and a
render-on-change game that skips them leaves the resumed session staring at
nothing until something happens to move.

The frame-delta layer already makes CLEAN-but-resent frames cheap on the
wire; dirty tracking makes them free in CPU too. Allocation discipline
matters as much as ever — under `-gc=conservative` steady-state allocations
are GC pressure (pauses inside the callback deadline): compose into a reused
`kit.Frame`, write cells directly, avoid per-player-per-wake string
building.

## Smoke scripts: scripted screens

Every catalog game ships a `smoke.yaml` next to its source: a small
deterministic script that drives the game and dumps named 80×24 screens. The
games repo CI runs it on every PR and posts the screens as a visual preview —
and it's a handy inner-loop tool ("show me the reveal screen" without
replaying the whole game by hand):

```sh
go run . -smoke smoke.yaml -smoke-out shots/   # native: no TinyGo needed
shellcade-kit smoke .                          # the same script vs the real wasm
```

A script declares the deterministic inputs and the steps:

```yaml
seed: 42        # required — fixed RNG seed
seats: 2        # required, 1..8 — all seats join before the first step
heartbeat: 50ms # optional — wake cadence during `advance`
config:         # optional — per-game config values
  variant: classic
steps:
  - shot: lobby       # dump the current screen(s), named "lobby"
  - rune: "5"         # printable rune → current seat
  - key: enter        # enter/backspace/esc/tab/up/down/left/right/space
  - text: "hello"     # sugar: one rune per character (typing games)
  - seat: 1           # switch the current input seat (sticky hot-seat)
  - advance: 1.5s     # sweep the virtual clock, one wake per heartbeat
  - wake:             # a single wake without moving the clock
  - shot: reveal
    seats: [0, 1]     # optional filter; default = every seat
```

The room is fully virtual: the clock starts at a seed-derived epoch and moves
**only** through `advance` (which wakes your game once per heartbeat — so
`advance: 1.5s` at 50ms is exactly 30 wakes), and the RNG comes from `seed`.
Two runs produce byte-identical shots; the wasm and native paths agree the
same way `shellcade-kit check`'s determinism gate guarantees.

Shots are per-seat (`01-lobby.seat0.ansi` + a plain-text `.txt` twin) and
collapse to a single file when every seat sees the same screen — broadcast
games never produce duplicates. `cat` an `.ansi` file to view it in your
terminal.

Authoring tips:

- **Shoot quiescent or exact-tick moments.** Mid-animation screens are
  deterministic too, but pick ticks that look intentional: land `advance` on
  the frame you mean to show (reels stopped, card slid in, flash visible).
- **Capture the moments a reviewer cares about**: the opening screen, a
  mid-game state with real play on the board, and the payoff (win/reveal/
  results) — three to six shots is plenty.
- **Per-player games**: shoot after the moment views diverge (deal, bets,
  reveal) so the preview shows what each seat sees.
- `advance` must be a whole number of heartbeats — the parser rejects
  ambiguous durations rather than rounding.
- **Smoke scripts drive at most 8 seats** — a screen-preview tool, not a
  load harness. Large-room games (up to the platform's 1..1024 bound) still
  pass smoke: the runner clamps your `MinPlayers` to the scripted seat count,
  and large-room behavior is exercised by `shellcade-kit check`'s budget
  gates, not by smoke screens.

The `smoke` package exposes the same machinery as Go API (`smoke.Parse`,
`smoke.Run`, `smoke.RenderANSI`) if you want shots inside your own tests.

## The full loop

| Stage | Command | What it proves |
|---|---|---|
| iterate | `go run .` (~0.1s) | gameplay, rendering, logic |
| artifact | `tinygo build -opt=1 -no-debug -gc=conservative -target wasip1 -buildmode=c-shared .` (~4s) | the real wasm builds |
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
| `Now` | room clock | wall-clock natively, virtual under `-seed` (see Native clock) |
| `Settled` | has the room ended? | |
| `Send(p, *Frame)` | one player's view | copies immediately — reuse the frame |
| `Identical(*Frame)` | same view to everyone | |
| `SetInputContext` | Nav / Command / Text | governs Back/q resolution |
| `End(Result)` | settle once and finish | round-based games |
| `Post(Result)` | record scores, keep playing | endless/social games |
| `Log(msg)` | host-side room log | visible in `shellcade-kit play` stderr |
| `Services()` | KV / config / accounts | see Durable state |

For unit tests, `github.com/shellcade/kit/v2/kittest` is an in-memory `Room` +
`Services` with a virtual clock, seeded RNG, and recorded
frames/posts/settles — drive your `Handler` directly and assert. Its
`KVUnavailable` knob replays the host's KV degradation (reads come back
missing, writes silently drop — see Durable state) so you can prove your
wallet code survives a store blip.

## What your game can't do (on purpose)

No filesystem, no network, no system clock, no threads: the sandbox gives
your game exactly the eleven host functions in ABI.md §3 and nothing else.
Resource budgets (memory cap, per-callback deadline) are enforced and
reported by `shellcade-kit check` — stay comfortably inside them and an arcade full
of other games stays healthy around yours.

## Study a complete game

The [public games catalog](https://github.com/shellcade/games) is the example
gallery: every game there is published and conformance-green. The best
all-rounder to read is
[pokies](https://github.com/shellcade/games/tree/main/games/bcook/pokies) — a
1–5 player slot machine exercising every feature: wake-driven reel animation,
staggered one-shots, admin-tunable odds via config, a durable wallet with
merge rules, and per-player rendering. It's a faithful port of the arcade's own
native pokies, so it reads like production code, because it is. Scaffold your
own with `shellcade-kit new mygame` and grow it from there.
