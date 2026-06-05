# shellcade game ABI — v2

This is the normative contract between a shellcade wasm game (the **guest**)
and the arcade (the **host**). The `wire` package in this module is this
document as code; a guest SDK in any language is implementable from this
document alone. All integers are **little-endian**. Strings are
`u16 length || UTF-8 bytes`.

Transport is [Extism](https://extism.org): guest exports are Extism plugin
functions (payloads via Extism input/output), host functions live in the
import namespace **`extism:host/user`**, and pointer-typed values are Extism
memory offsets (allocate, pass, free).

> **What changed from v1.** v2 is a MAJOR. The frame is no longer a fixed packed
> grid: `send`/`identical` now carry a variable-length **frame-delta container**
> (§4.5) and **return a `u32` epoch**. The cell grew from 16 to **24 bytes** to
> carry grapheme clusters (§4.3). There is no dual loader and no v1 ingestion
> path — a host built for v2 refuses a major-1 artifact, and the kit module major
> bumps in lockstep. See §5 for what this buys future minors.

## 1. Execution model

- One plugin **instance == one room**. Instantiation precedes `start`; the
  instance is destroyed after `close`. Two rooms share nothing.
- Callbacks are invoked **serially** — never concurrently — per room.
- **Guest code runs only during host calls.** Language built-in timers,
  goroutines, and event loops never fire between callbacks.
- The host calls `wake` at a host-owned heartbeat while the room has at least
  one connected player, and never when it is empty. All time-driven behavior
  (countdowns, clocks, animation) derives from comparing guest-held deadlines
  against CallContext time on `wake`. The `wake` cadence is host-owned and
  unspecified (treat it as "roughly periodic, may jitter"); never assume a
  fixed interval. `nowUnixNanos` (§4.1) is **monotonic non-decreasing** across
  the callbacks of a single room instance — two callbacks may carry the same
  instant, but a later callback never carries an earlier one.
- Games render **on change**: compose a frame and call `send`/`identical`
  from any callback. There is no recomposition callback; the host coalesces
  (depth-1, newest-wins) per consumer. Sending **zero frames before the first
  join is tolerated** — the host does not expect or require an initial render.
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
| `shellcade_abi` | — | 4 bytes: u32 ABI major version (`2`) |
| `meta` | — | packed Meta (§4.2) |
| `start` | Ctx | — |
| `join` | Ctx ‖ u32 playerIdx | — |
| `leave` | Ctx ‖ u32 playerIdx | — |
| `input` | Ctx ‖ u32 playerIdx ‖ u8 kind ‖ u32 rune ‖ u8 key | — |
| `wake` | Ctx | — |
| `close` | Ctx | — |

`playerIdx` indexes the roster carried in that callback's Ctx and is valid
only for that callback. For `leave`, the departed player is appended as the
**final** roster entry and `memberCount` **includes** that departed entry (it
is no longer a member, but it is present in the roster for the duration of the
`leave` callback). Input `kind`: 0 = printable rune (read `rune`), 1 = named
key (read `key`: 1 Enter, 2 Backspace, 3 Esc, 4 Tab, 5 Up, 6 Down, 7 Left,
8 Right, 9 Ctrl-C).

Guests MUST tolerate input they do not understand: an unknown `kind`, an
unknown `key`, or trailing bytes beyond the fields listed above (§5). A
non-zero exit status or trap from any export is a fault: the host settles the
room (players flagged) and destroys the instance.

## 3. Host functions (`extism:host/user`)

Effects are honored only while a guest callback is on-stack; outside one they
are no-ops. `ptr` parameters/returns are Extism memory offsets; a `0` return
means not-found.

| Function | Signature | Semantics |
|---|---|---|
| `send` | (i64 playerIdx, ptr payload) → i64 epoch | deliver a frame-delta container (§4.5) to one player; returns the slot epoch (low 32 bits) |
| `identical` | (ptr payload) → i64 epoch | deliver one frame-delta container to every player; returns the broadcast epoch (low 32 bits) |
| `set_input_context` | (i64 ctx) | 0 Nav · 1 Command · 2 Text (Back/q resolution) |
| `end` | (ptr result) | settle the room exactly once (§4.4) |
| `post` | (ptr result) | record a leaderboard result without ending |
| `log` | (i64 level, ptr msg) | 0 debug · 1 info · 2 warn · 3 error |
| `kv_get` | (i64 playerIdx, ptr key) → ptr | per-user durable KV read |
| `kv_set` | (i64 playerIdx, ptr key, ptr val, ptr rule) | rule: `keep-winner` `keep-loser` `sum` `max`; for `sum`/`max` the value MUST be a base-10 ASCII int64 (unparsable values degrade to keep-winner at merge time) |
| `kv_delete` | (i64 playerIdx, ptr key) | |
| `config_get` | (ptr key) → ptr | read-only per-game config |
| `profile_get` | (i64 playerIdx) → ptr | lifetime stats (reserved; may return 0) |

`send` and `identical` return an `i64` whose **low 32 bits carry the epoch**
the guest MUST stamp its baseline with for that slot; the **upper 32 bits are
reserved-zero** and a guest MUST read only the low 32 bits (§4.6, §5).
`set_input_context` sets **host-side** state, not guest linear memory, and
therefore **survives hibernation** (§8) — a guest does not re-issue it on
resume.

Scoping is host-side: the guest names only a roster index and a key — the
account and the game's namespace are derived by the host. A guest cannot
address another game's data or a non-member account.

## 4. Payload encodings

### 4.1 CallContext (Ctx)

```
i64 nowUnixNanos      room clock (== virtualized WASI clock), monotonic non-decreasing
i64 seed              room RNG seed
u8  seedSet           0/1
u8  mode              0 quick · 1 private · 2 solo
u16 capacity
u16 minPlayers
u16 memberCount       on `leave`, includes the departed entry
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

### 4.3 Frame (the delta container and its cell)

A frame is delivered as a **frame-delta container** (§4.5), a variable-length
run-list over a fixed **24 rows × 80 cols** grid (1920 cells, row-major). The
container's steady-state form is a small delta; its **keyframe form** is the
only full-frame payload. Both `send` and `identical` carry this container by
Extism memory offset.

The packed cell is **24 bytes**, little-endian, anchor layout:

```
u32 rune     base code point                      @0
u32 cp2      extra grapheme code point (0=unused)  @4
u32 cp3      extra grapheme code point (0=unused)  @8
u8  fgSet · u8 fgR · u8 fgG · u8 fgB               @12..15
u8  bgSet · u8 bgR · u8 bgG · u8 bgB               @16..19
u8  attr  (bit0 bold · bit1 dim · bit2 underline · bit3 reverse)   @20
u8  cont  (1 = continuation column of a wide glyph)               @21
u16 pad   (zero)                                  @22..23
```

`cp2`/`cp3` carry the extra code points of a grapheme cluster — a VS16
variation selector (U+FE0F), a skin-tone modifier (U+1F3FB..U+1F3FF), a keycap
combiner (U+20E3), or a ZWJ piece. A cluster needing **more than three code
points** (a family ZWJ emoji) is **unsupported**: a producer SHALL refuse it
(draw nothing), never truncate it to a different valid-looking glyph (§4.3a).

A field-order change is permitted only with justification and MUST NOT change
the 24-byte size or the grapheme capability.

#### 4.3a Canonical-zero rule (normative)

**Unused `cp2`/`cp3` slots and `pad` MUST be zero**, so that two cells are equal
exactly when their 24 bytes are equal — a single `memcmp`. This is load-bearing
in **two** independent ways:

1. **Delta determinism (a producer obligation, not a nicety).** A delta encoder
   marks a cell changed by comparing 24 bytes. A stray bit in an unused cp slot
   or in `pad` spuriously marks a cell changed and **shifts run boundaries**, so
   two producers that disagree on those bytes emit different — both "valid" —
   bytes on the wire. Canonical-zero is what makes cross-implementation byte
   output well-defined.
2. **Hibernation byte-identity (§8).** Identical authoring calls must produce
   identical baselines must produce a byte-identical reconstructed frame after a
   restore. Canonical-zero is the precondition that makes "byte-identical" a
   well-defined claim.

The **wire encode path** (`PutCell` in `wire`; the equivalent in any guest) is
the normative enforcer: it SHALL always write `pad = 0` and SHALL write the cp
slots verbatim (0 = unused) **regardless of the in-memory cell contents**, so
even a hand-built cell with garbage in a slot it does not use serializes
canonically. A producer that leaves dirty `pad`/cp bytes corrupts **its own**
diff against the host's baseline; the host does not (and cannot) re-canonicalize.

### 4.4 Result

```
u16 rankingCount
  per ranking: u32 playerIdx · i64 metric · u16 rank
               · u8 status (0 finished · 1 dnf · 2 flagged)
```

`playerIdx` indexes the **current callback's** roster. If a result names an
account id that is no longer a member of that roster (e.g. a settled player), a
producer SHALL fall back to **index 0** rather than fail.

### 4.5 Frame-delta payload encoding (normative)

A frame-delta container is **variable-length, little-endian, index-addressed**:

```
Header (9 bytes):
  u8  flags        bit0 = keyframe (1 = this payload is a full-frame keyframe);
                   all other bits MUST be zero (§5)
  u32 epoch        the epoch this delta is computed against (host-issued).
                   ALWAYS present and fixed-width; the guest writes the epoch the
                   host last returned for this slot (0 on a fresh instance), so
                   payload byte length is epoch-independent.
  u16 runCount     number of runs (keyframe: exactly 1; no-change: 0)
  u8  rows         grid geometry; MUST be 24 in v2 (host drops on mismatch)
  u8  cols         grid geometry; MUST be 80 in v2 (host drops on mismatch)
Then runCount runs, each:
  u16 startIndex   first cell index, 0..(Rows×Cols−1), == row×Cols + col
                   (a CELL index, NOT a byte offset)
  u16 runLen       1..Rows×Cols, count of consecutive changed cells
  runLen × 24B     packed §4.3 cells (canonical-zero), byte-identical to the
                   §4.3 cell layout
```

Run bounds are expressed in terms of `Rows×Cols`, never the literal 1920, so a
future minor may accept additional geometries without a wire break.

**Acceptance is the normative envelope.** The host SHALL accept ANY structurally
valid container — runs strictly ascending and non-overlapping
(`startIndex[i] ≥ startIndex[i−1] + runLen[i−1]`), every run in-bounds
(`startIndex + runLen ≤ Rows×Cols`), `runCount` consistent with the body length
(`9 + Σ(4 + runLen×24) == len`), geometry `(24, 80)`, and no unknown flag bit
set — whose **epoch matches** the slot (§4.6). The host does NOT verify that runs
are minimal, greedy, or even true diffs against its baseline.

**Reference encoders** (this kit's SDK and the published cross-language encoder)
SHALL emit the **maximal span of consecutive changed cells, greedy
left-to-right, with gap = 0**: a single unchanged cell between two changed spans
forces two runs — **no run splitting, no gap-merge**. That determinism is what
makes the cross-language golden vectors byte-identical. It binds reference
encoders only (see §4.7).

Canonical forms:

- **`runCount == 0`** is the canonical **no-change** delta: the 9-byte header
  alone. It is NOT a zero-length payload and NOT a keyframe; the host applies
  nothing.
- **Keyframe** = a 9-byte header `{flags bit0 = 1, runCount = 1}` + one run
  `{startIndex = 0, runLen = 1920}` + the full 1920×24 = 46080-byte grid =
  **46093 bytes**. `runLen` counts cells, so the cells that follow are
  `runLen × 24` bytes; 1920 < 65536 fits a `u16`. This is the ONLY full-frame
  form — there is no `runCount = 0` keyframe and no absent-run-table form.
- **Length bound:** `9 + runCount×4 + changedCells×24`. A producer SHALL apply a
  budget check: if an encoded delta would meet or exceed **46093 bytes**, it
  SHALL send the keyframe form instead. The threshold is **inclusive (`≥`)**: a
  full-change one-run delta is itself exactly 46093 bytes (= the keyframe size),
  and the inclusive `≥` makes two conformant encoders agree on shipping the
  canonical keyframe at that exact boundary. The wire cost is bounded at exactly
  46093 bytes, never worse (+1.50× over v1's 30720-byte floor — the one honest
  regression, paid only on full repaints / bootstrap / resume).

**Validation (host-side, drop-not-fatal).** A validator SHALL check length
consistency with `runCount`, every run in-bounds, runs ascending and
non-overlapping, geometry `(24, 80)`, and no unknown flag bit; a **short read**
SHALL degrade to a malformed-delta error, **never** a panic and **never** an
out-of-bounds read (this matters for a from-scratch guest: panic-on-short-read
turns a malformed payload into a room fault). On a malformed delta the host
logs, drops it, **bumps the slot epoch, and returns the new epoch** (§4.6).
Applying a delta is in-place over the host's previous packed grid — copy each
run's cells in at `startIndex × 24`; a keyframe (one full-cover run) overwrites
all 1920 cells. Application is `O(changed cells)`, allocation-free, and never
partially mutates the baseline on a malformed container.

The round-trip invariant `apply(base, diff(base, next)) == next` SHALL hold for
arbitrary 24-byte frame pairs, including full-change and zero-change. There is
no magic sentinel byte: the structural validator is sufficient.

### 4.6 Epoch and baseline authority (normative)

The **host is the sole authority** on baseline validity. Per consumer slot —
each roster index, plus one broadcast slot — the host holds a previous packed
grid, an `epoch`, and a present flag.

- A **non-keyframe** delta is applied **iff** its header epoch equals the slot's
  current epoch **AND** the slot has a baseline. Otherwise (epoch mismatch,
  malformed, or no baseline) the host **drops** the delta, **bumps** the slot
  epoch, and **returns the new epoch**.
- A **keyframe** is accepted regardless of epoch (it is self-contained), sets the
  slot baseline present, and adopts the header epoch.
- Every call **returns** the epoch the guest must stamp its baseline with.

The **guest mirrors** the returned epoch. If the returned epoch differs from the
one it sent (the host rejected the delta), the guest's **next send to that slot
is a forced keyframe**. The guest never decides baseline validity — it only
mirrors the host's authority. This makes every failure mode **degrade-to-drop**
(one frame re-sent, likely coalesced away), never a corrupt frame, and closes
the desync hole that a guest-side roster/account inference would leave open — in
particular the solo / same-account rehydrate after hibernation (§8).

`identical` is diffed against the **broadcast** slot; on a successful apply the
host reconciles **every** per-index baseline (copies the reconstructed grid into
each, sets each present, stamps each with the broadcast epoch). A guest that
broadcasts SHALL mirror this — stamp every per-index slot with the returned epoch
and copy the broadcast frame into every per-index baseline — so a later
per-player `send` diffs against the correct baseline (§4.7, obligation 4).

Any **roster mutation** (join, leave, index shift, mid-room joiner — indices
renumber) bumps the epoch and marks affected slots not-present, so the next send
to each affected slot is epoch-rejected and the guest sends a keyframe (the RFB
`incremental=0` analogue).

### 4.7 Hand-rolled guests

A guest MAY bypass any SDK and construct containers itself — including emitting
runs **directly from game knowledge** (`O(changed cells)`, no full-frame compose,
no 1920-cell scan). It is judged on **reconstructed frames**, never on wire
bytes: only reference encoders are held to golden-vector byte-identity.
Conformance for a hand-rolled guest is "delta run reconstructs byte-identical to
a keyframe control" and "hibernation byte-identical", not "emits the same bytes
as the reference encoder". The four obligations such a guest MUST honor are
exactly the host envelope:

1. **Canonical-zero cells** (§4.3a) — a stray pad/cp bit silently diverges the
   guest's baseline from the host's, corrupting the guest's own later runs.
2. **Epoch discipline** (§4.6) — stamp the host-returned epoch; send a keyframe
   on first send, on any rejection, and after any roster change.
3. **Completeness is the guest's problem** — a changed cell never shipped stays
   stale for viewers until the next keyframe; the host cannot detect it.
4. **`identical` reconciles all slots** — a guest mixing broadcast and
   per-player sends owns the all-slot baseline reconciliation the SDK does for
   free.

Mixing custom sends with SDK sends cannot desync permanently: a stale-epoch send
is rejected and auto-keyframes — the epoch mechanism makes bad interleavings
self-healing. (This kit's Go SDK deliberately exposes `Room.Send`/`Room.Identical`
only — no run-level API; if demand appears, an additive run-level writer is a kit
**minor** with zero ABI work.)

## 5. Versioning and evolution (normative)

`shellcade_abi` returns the major version; the host refuses mismatches at load
time. **v2 is a MAJOR**: the frame encoding changed (24-byte grapheme cell +
delta container), so there is no minor/additive framing of it and no backward
interop — a v2 host refuses a major-1 artifact outright, with no dual loader and
no capability gate.

v2 pays for future flexibility up front so later changes can be **minors**. The
following rules are normative:

1. **Tolerant guest inputs.** Guests MUST ignore unknown input `kind` and `key`
   values, and MUST ignore trailing bytes beyond the fields they know in every
   guest-export payload. This converts future input growth (mouse events, paste,
   focus, new named keys) from a major into a minor.
2. **Unknown `attr` bits are ignored by renderers.** 4 of the 8 cell `attr` bits
   are assigned (§4.3); a host MUST render the bits it knows and ignore the rest,
   so italic / strikethrough / blink-class additions are additive minors.
3. **Unassigned `flags` bits and cell `pad` MUST be zero — and are rejected
   until assigned.** The 7 unassigned header `flags` bits and the per-cell `pad`
   `u16` MUST be zero in v2; the host REJECTS a container with any unknown flag
   bit set (drop + epoch bump, like any malformed delta) rather than silently
   ignoring it. A future minor MAY assign meaning to a flag bit and, gated by it,
   to `pad` bytes (e.g. a hyperlink-table index or tile id).
4. **Epoch return spare bits.** `send`/`identical` return an `i64` whose low 32
   bits carry the epoch; the **upper 32 bits are reserved-zero** and guests MUST
   read only the low 32. That reserves a free host→guest signaling channel for a
   later minor (backpressure hints, viewer capabilities) with no new host
   function.
5. **Deploy-order rule.** These reject-unknown policies are safe because **the
   host always upgrades before artifacts**: a guest artifact may assume the host
   understands every feature of the kit version it was built against, and the
   host advertises nothing. That ordering is what lets a flag-gated feature ship
   as a minor — every prior host already rejected the flag while it was
   unassigned — without resurrecting a capability gate.

Consciously rejected (so they are not relitigated): **>3 code points per cell**
(family ZWJ emoji — the future path, if ever needed, is a flag-gated side table,
not a wider cell taxing every keyframe); **compression** of deltas (they are
tens to a few thousand bytes); a **variable cell schema** (the fixed 24-byte cell
is what keeps the diff a `memcmp` and a from-scratch encoder tiny).

## 6. Build rules (hard-won, normative for Go guests)

- **Frames pass by pointer** in SDKs. A by-value ~46KB frame struct explodes
  into thousands of wasm locals: ~50× compile time, ~15× artifact size, and
  optimizer OOMs.
- TinyGo: dev profile `-opt=1 -no-debug -gc=leaking -target wasip1
  -buildmode=c-shared` (~seconds). Release profile `-opt=2` (CI only).
  `-opt=0` is unsupported (oversized functions crash wazero's arm64 backend).
  `-gc=leaking` is the interim profile while TinyGo's conservative GC fault is
  tracked; SDKs must keep the steady state allocation-free (reused encode and
  baseline buffers, freed Extism allocations). v2's per-consumer baseline table
  (one packed 24-byte-cell grid per roster slot + a broadcast slot + a
  keyframe-sized delta scratch) is reused package globals written by index, not
  growing buffers — the steady-state diff allocates nothing beyond the (now
  delta-sized) Extism staging copy, immediately freed.
- Keep per-callback work bounded: the host enforces a wall-clock deadline per
  callback and a linear-memory cap per instance. The v2 baseline table is
  ~0.85 MB worst case at 24 bytes per cell — far under the cap.

## 7. Non-Go guests (proved with a Rust fixture)

The ABI is language-neutral; a Rust guest built from this document alone passes
full conformance, including hibernation determinism and the v2 delta container
(an independent Rust RUN-LIST encoder was cross-verified **byte-identical** to
this kit's reference encoder across real and synthetic frame sequences, including
a grapheme-churn sequence exercising `cp2`/`cp3`). Contract clarifications from
those exercises:

- **Do NOT use the extism-pdk `#[host_fn]` macro for the §3 host functions.**
  It heap-wraps every declared argument and passes a memory OFFSET, so scalar
  parameters (roster indices, log levels) arrive corrupted, and it cannot express
  the §3 **return values** (`send`/`identical` now return an `i64` epoch).
  Declare the host functions as raw wasm imports instead, passing scalars
  directly, buffer parameters as Extism memory offsets, and reading the returned
  epoch from the low 32 bits:

  ```rust
  #[link(wasm_import_module = "extism:host/user")]
  extern "C" {
      fn send(player_idx: u64, payload_off: u64) -> u64; // low 32 bits = epoch
      fn identical(payload_off: u64) -> u64;             // low 32 bits = epoch
      fn log(level: u64, msg_off: u64);
      // …the rest of §3, same shapes
  }
  ```

  The PDK remains useful for kernel plumbing only: `extism::load_input`,
  `Memory::from_bytes(..).offset()` / `Memory::find(off)` / `free`, and
  `set_output`.
- **Exports are bare `#[no_mangle] pub extern "C" fn name() -> i32`** (0 = ok),
  matching §2 — not `#[plugin_fn]`, which imposes its own input/output
  handling. Build as `cdylib` for `wasm32-wasip1`.
- **Decoders must degrade, never panic.** Clamp a string's byte length to
  `0xffff`; on a short read of any payload, return zero/empty rather than panic
  (`panic = abort` turns a malformed `Ctx` into a room fault). The delta
  validator (§4.5) follows the same drop-not-fatal contract.

Both Go and Rust artifacts pass the same conformance script; v2's wider 24-byte
cell raises a keyframe to 46093 bytes and the worst-case baseline memory to
~0.85 MB on the guest, both still far under the 32 MiB linear-memory cap — pick
your language for ergonomics, not budgets.

## 8. The hibernation contract (one sentence, plus one carve-out)

Your room must be fully reconstructable from **guest linear memory + the
RoomConfig + the CallContext** — never derive behavior from anything else (host
wall-time offsets, ambient entropy, import-time state). The conformance harness
verifies this: it snapshots your room mid-script, restores it into a fresh
instance of the **identical artifact**, and requires **byte-identical frames
thereafter**.

Byte-identity is well-defined because cells are **canonical-zero** (§4.3a):
identical authoring calls produce identical packed bytes. **No guest hibernation
logic is required for frame diffing**: the host's per-consumer baseline cache is
ephemeral host memory, not snapshotted; on resume the host re-seeds its epoch
strictly above any pre-snapshot epoch and marks every slot not-present, so the
restored guest's first delta — computed against its snapshot-surviving baseline
and stamped with its snapshot-surviving epoch — epoch-mismatches and is rejected,
forcing a keyframe and a byte-identical full grid. This holds even for the
hardest case, a **solo or full-roster rehydrate where the same account set
returns** (an account-id comparison would see no change and wrongly keep the old
baseline; the host epoch re-seed removes any dependence on the guest inferring
anything).

**Carve-out:** "linear memory only" applies to **guest** state. Host-side state
set via `set_input_context` (§3) is **not** in linear memory and DOES survive
hibernation; a guest does not re-issue it on resume.
