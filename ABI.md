# shellcade game ABI — v1

This is the normative contract between a shellcade wasm game (the **guest**)
and the arcade (the **host**). The `wire` package in this module is this
document as code; a guest SDK in any language is implementable from this
document alone. All integers are **little-endian**. Strings are
`u16 length || UTF-8 bytes`.

Transport is [Extism](https://extism.org): guest exports are Extism plugin
functions (payloads via Extism input/output), host functions live in the
import namespace **`extism:host/user`**, and pointer-typed values are Extism
memory offsets (allocate, pass, free).

## 1. Execution model

- One plugin **instance == one room**. Instantiation precedes `start`; the
  instance is destroyed after `close`. Two rooms share nothing.
- Callbacks are invoked **serially** — never concurrently — per room.
- **Guest code runs only during host calls.** Language built-in timers,
  goroutines, and event loops never fire between callbacks.
- The host calls `wake` at a host-owned heartbeat while the room has at least
  one connected player, and never when it is empty. All time-driven behavior
  (countdowns, clocks, animation) derives from comparing guest-held deadlines
  against CallContext time on `wake`.
- Games render **on change**: compose a frame and call `send`/`identical`
  from any callback. There is no recomposition callback; the host coalesces
  (depth-1, newest-wins) per consumer.
- **State model**: a room's entire guest state MUST live in linear memory /
  globals (there is nowhere else). The host may snapshot linear memory at a
  quiescent point and later restore it into a fresh instance of the
  **identical artifact** (hibernation). Connection tokens change across
  hibernation — key persistent state by **account id**, never by connection.
- The room clock, WASI entropy, sleep, and stdio are **virtualized** by the
  host: `clock_time_get` returns room time (== CallContext time), `random_get`
  is seeded from the room seed, sleep returns immediately, stdout/stderr go to
  the host log. Filesystem and network are unreachable.

## 2. Guest exports

| Export | Input payload (after CallContext where noted) | Output |
|---|---|---|
| `shellcade_abi` | — | 4 bytes: u32 ABI major version (`1`) |
| `meta` | — | packed Meta (§4.2) |
| `start` | Ctx | — |
| `join` | Ctx ‖ u32 playerIdx | — |
| `leave` | Ctx ‖ u32 playerIdx | — |
| `input` | Ctx ‖ u32 playerIdx ‖ u8 kind ‖ u32 rune ‖ u8 key | — |
| `wake` | Ctx | — |
| `close` | Ctx | — |

`playerIdx` indexes the roster carried in that callback's Ctx and is valid
only for that callback. For `leave`, the departed player is appended as the
**final** roster entry (it is no longer a member). Input `kind`: 0 = printable
rune (read `rune`), 1 = named key (read `key`: 1 Enter, 2 Backspace, 3 Esc,
4 Tab, 5 Up, 6 Down, 7 Left, 8 Right, 9 Ctrl-C).

A non-zero exit status or trap from any export is a fault: the host settles
the room (players flagged) and destroys the instance.

## 3. Host functions (`extism:host/user`)

Effects are honored only while a guest callback is on-stack; outside one they
are no-ops. `ptr` parameters/returns are Extism memory offsets; a `0` return
means not-found.

| Function | Signature | Semantics |
|---|---|---|
| `send` | (i64 playerIdx, ptr frame) | deliver a frame (§4.3) to one player |
| `identical` | (ptr frame) | deliver one frame to every player |
| `set_input_context` | (i64 ctx) | 0 Nav · 1 Command · 2 Text (Back/q resolution) |
| `end` | (ptr result) | settle the room exactly once (§4.4) |
| `post` | (ptr result) | record a leaderboard result without ending |
| `log` | (i64 level, ptr msg) | 0 debug · 1 info · 2 warn · 3 error |
| `kv_get` | (i64 playerIdx, ptr key) → ptr | per-user durable KV read |
| `kv_set` | (i64 playerIdx, ptr key, ptr val, ptr rule) | rule: `keep-winner` `keep-loser` `sum` `max` |
| `kv_delete` | (i64 playerIdx, ptr key) | |
| `config_get` | (ptr key) → ptr | read-only per-game config |
| `profile_get` | (i64 playerIdx) → ptr | lifetime stats (reserved; may return 0) |

Scoping is host-side: the guest names only a roster index and a key — the
account and the game's namespace are derived by the host. A guest cannot
address another game's data or a non-member account.

## 4. Payload encodings

### 4.1 CallContext (Ctx)

```
i64 nowUnixNanos      room clock (== virtualized WASI clock)
i64 seed              room RNG seed
u8  seedSet           0/1
u8  mode              0 quick · 1 private · 2 solo
u16 capacity
u16 minPlayers
u16 memberCount
  per member: str handle · str accountID · str conn · u8 kind (0 guest · 1 member)
u8  settled           0/1
```

### 4.2 Meta

```
str slug · str name · str shortDescription
u16 minPlayers · u16 maxPlayers
u16 tagCount · per tag: str
str quickModeLabel · str soloModeLabel · str privateInviteLine   ("" = default)
u8  hasLeaderboard
  if 1: str metricLabel · u8 direction (0 higher · 1 lower)
        · u8 aggregation (0 best · 1 sum) · u8 format (0 int · 1 decimal · 2 duration)
```

`slug` must be non-empty; the host refuses artifacts whose slug or version it
cannot accept.

### 4.3 Frame

Fixed-size: **24 rows × 80 cols × 16 bytes = 30720 bytes**, row-major. Cell:

```
u32 rune
u8 fgSet · u8 fgR · u8 fgG · u8 fgB
u8 bgSet · u8 bgR · u8 bgG · u8 bgB
u8 attr (bit0 bold · bit1 dim · bit2 underline · bit3 reverse)
u8 cont (1 = continuation column of a wide rune)
u16 pad (zero)
```

A wrong-length frame payload is dropped by the host (logged, never fatal).

### 4.4 Result

```
u16 rankingCount
  per ranking: u32 playerIdx · i64 metric · u16 rank
               · u8 status (0 finished · 1 dnf · 2 flagged)
```

`playerIdx` indexes the **current callback's** roster.

## 5. Versioning

`shellcade_abi` returns the major version; the host refuses mismatches at load
time. Additive evolution (new host functions, new trailing Meta fields guarded
by presence flags) is a minor version and requires nothing from existing
artifacts. Breaking changes bump the major.

## 6. Build rules (hard-won, normative for Go guests)

- **Frames pass by pointer** in SDKs. A by-value ~46KB frame struct explodes
  into thousands of wasm locals: ~50× compile time, ~15× artifact size, and
  optimizer OOMs.
- TinyGo: dev profile `-opt=1 -no-debug -gc=leaking -target wasip1
  -buildmode=c-shared` (~seconds). Release profile `-opt=2` (CI only).
  `-opt=0` is unsupported (oversized functions crash wazero's arm64 backend).
  `-gc=leaking` is the interim profile while TinyGo's conservative GC fault is
  tracked; SDKs must keep the steady state allocation-free (reused encode
  buffers, freed Extism allocations).
- Keep per-callback work bounded: the host enforces a wall-clock deadline per
  callback and a linear-memory cap per instance.
