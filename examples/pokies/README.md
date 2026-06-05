# Pokies — the reference game (and the wake-idiom cookbook)

Pokies is a 1–5 player slot machine and the shellcade devkit's reference game:
a faithful port of the arcade's native cabinet to the wasm ABI. It exercises
every feature an author reaches for — per-player rendering, a durable wallet
with merge rules, admin-tunable odds via config, and a leaderboard `Post` — but
its real job is to be **the canonical answer to one question**: *if my game
can't use timers, how does anything happen over time?*

```sh
go run .                      # play it in your terminal (native dev loop)
go run . -seats 3             # three machines; Ctrl-T switches seats
go run . -seed 42             # reproducible: virtual clock, fixed RNG
```

## The one rule that shapes everything

Your code runs **only inside callbacks**, and the sandbox has **no timers, no
goroutines, no `time.Sleep`**. The only callback that fires without player
input is **`OnWake`** — the host heartbeat, ~20×/second while anyone is
connected. So every "later…" becomes a **comparison against `r.Now()` inside
`OnWake`**.

`r.Now()` is the room clock the host owns. It is monotonic-enough for game
logic, identical to what your language's `time.Now()` would return (the arcade
virtualizes the clock), and — under `go run . -seed N` — a deterministic
virtual clock that advances one heartbeat per wake. Build your time logic
against `r.Now()` and it behaves identically native, in wasm, and under test.

The four idioms below are everything. Each is live in
[`room.go`](room.go); line ranges are pointers, not promises (read the code).

---

## 1. One-shot deadline — porting `time.AfterFunc`

**Want:** do something once, T later. **Don't:** start a timer. **Do:** store
the deadline now; check it every wake; act when `Now()` passes it.

```go
// set the deadline when the event happens (settleSpin, room.go ~339):
m.flashUntil = r.Now().Add(flashDur)

// expire it on the heartbeat (OnWake, room.go ~201):
if m.flash != "" && now.After(m.flashUntil) {
    m.flash = ""
}
```

The state (`flashUntil`) lives in your room struct, so it survives a
hibernation freeze/thaw — a real `AfterFunc` would not. This is the whole
pattern; a "flash a banner for 1.5s" is the same shape as "expire a power-up".

## 2. Staggered schedule — many one-shots, precomputed

**Want:** several things to land at offset times (reel 0 at +150ms, reel 1 at
+400ms, …). **Do:** derive each deadline from one start time and an index;
land whatever is due this wake.

```go
// in OnWake (room.go ~207): land every reel whose deadline has passed.
for i := m.spin.landed; i < 3; i++ {
    due := m.spin.startedAt.Add(reelStopBase + time.Duration(i)*reelStopStep)
    if !now.After(due) {
        break // not due yet, and later reels are even later — stop
    }
    rm.landReel(r, id, i)
}
```

Because deadlines are **derived from `startedAt`**, a slow frame, a paused tab,
or a hibernation gap can never desync them: when wakes resume, every overdue
reel lands in order on the next heartbeat. Never advance an index "per wake" —
that couples your animation to the (variable) heartbeat rate.

## 3. Animation clock — porting `time.Ticker`, derived not accumulated

**Want:** a smooth, repeating cadence (the reels scrolling while they spin).
**Do:** compute the current cycle from **elapsed time**, every render. Never
keep a counter you `++` each wake.

```go
// spinState.cycle (room.go ~41): which scroll frame are we on right now?
func (s *spinState) cycle(now time.Time) int {
    return int(now.Sub(s.startedAt) / cycleRate)
}
```

A derived clock is framerate-independent (looks right whether the heartbeat is
20Hz or 5Hz), reproducible under `-seed`, and hibernation-stable. The same
trick drives a repeating cadence: "fire every N" is `now.Sub(start) / N`
changing value — or, for the periodic config refresh, a rolling next-deadline:

```go
// OnWake (room.go ~196): re-read admin config on a slow cadence.
if now.After(rm.nextCfg) {
    rm.loadVariant(r)
    rm.nextCfg = now.Add(configRefresh)
}
```

## 4. Turn / countdown timeout — the deadline pattern, applied to a turn

A turn timer is just idiom #1 with a player-facing countdown. Store the
deadline when the turn begins; show `deadline.Sub(now)` in your frame; on the
wake where `now.After(deadline)`, end the turn (auto-fold, skip, time-out).

Pokies has no strict turn clock, but its **big-win ticker** is exactly this
shape — a banner with an expiry the renderer reads:

```go
// armed on a jackpot (settleSpin, room.go ~322):
rm.ticker = ticker{text: msg, until: r.Now().Add(tickerDur)}

// read while live (room.go ~356):
func (rm *room) tickerActive(now time.Time) bool {
    return rm.ticker.text != "" && now.Before(rm.ticker.until)
}
```

For a real turn timeout, swap "clear the banner" for "advance the turn" in the
`now.After(deadline)` branch of `OnWake`.

---

## "How do I port a timer?" — quick reference

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
   then `Send` the frame — see [`room.go`](room.go) `OnWake`, which expires
   flashes, lands reels, refreshes config, and calls `rm.render(r)` last.

For the SDK-level overview of these patterns, see the repo
[GUIDE.md](../../GUIDE.md) ("Time, the wake way"); this file is the worked,
line-referenced version.
