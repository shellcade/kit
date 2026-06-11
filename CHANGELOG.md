# kit

## 2.10.0

### Minor Changes

- 11886a9: Declared extra controls in `GameMeta` (wire revision 6): a new trailing
  presence-guarded meta section listing inputs beyond the canonical control
  vocabulary — a printable rune or a named key, each with a short display
  label — so front ends on devices without the corresponding physical key
  (touch) can surface each declaration as a tappable affordance that sends
  exactly the declared input. Presentation metadata only: declarations change
  no input interpretation, and games fully served by the canonical vocabulary
  need none. Go SDK: `GameMeta.Controls` + `kit.RuneControl` /
  `kit.KeyControl`; Rust SDK: `Meta::controls` + `ControlDecl`, encoded
  byte-identically (golden-pinned). Validation (`wire.ValidateControls`,
  enforced at `meta()` encode time): printable rune or assigned key code,
  non-empty label ≤16 runes, no duplicate inputs, ≤32 declarations. ABI.md
  §4.2 documents the section; GUIDE.md gains a mobile-friendly-controls
  authoring section.

## 2.9.0

### Minor Changes

- e4f2e4d: Per-member player character in the CallContext behind a new declared
  `CtxFeatCharacter` (1<<1) ctx feature: each roster member carries
  `str glyph · u8 ink RGB · u8 bg RGB · u8 asciiFallback` after its kind byte,
  in both member-bearing forms (wire revision 5). Go and Rust SDKs expose
  `Character` on `Player` and a `CharacterCell` / `character_cell` helper
  returning the one styled cell — every catalogue glyph is width 1, so games
  place a player's character with zero width logic. Non-declaring guests
  decode byte-identically.

## 2.8.0

### Minor Changes

- a0cefdf: `kittest.Room` gains an opt-in `KVUnavailable` chaos knob that replays the
  production host's KV degradation exactly: while set, `Get` reports every key
  as missing (`nil, false, nil`) and `Set`/`Delete` return `nil` without
  persisting — the ABI has no error channel, so a real store outage never
  surfaces a Go error. This lets authors test the read-absent-reinit hazard
  (the natural `Get → missing → initialize → Set` wallet pattern silently
  resets saved state during a store blip), previously impossible to simulate
  because the double's KV always succeeded. GUIDE.md's Durable state section
  now documents the degradation semantics and the conservative-missing-read
  guidance, and `ExampleRoom_kvUnavailable` demonstrates the failing pattern.
- a0cefdf: Wire-revision provenance: the packed Meta gains a trailing presence-guarded
  `u16 wireRevision` — a single monotonic counter of wire-visible minor
  additions (`wire.Revision`, currently 4; ledger in its docs and ABI.md §4.2),
  stamped automatically by both the Go and Rust guest SDKs and never set by
  the author. Old hosts ignore the bytes; artifacts built with older kits
  decode as revision 0 (unknown). This gives the deploy-order rule (ABI.md §5)
  its mechanical anchor: a host can now warn on or refuse artifacts declaring
  a revision above the one it implements instead of loading them blind, and
  record per-artifact contract provenance. Pure additive trailer following the
  established pattern — ABI major stays 2. The Rust SDK's `WIRE_REVISION` is
  pinned to `wire.Revision` by a new Go cross-check test
  (`wire.TestRustWireRevisionMatchesWire`), and the Go/Rust meta goldens are
  updated in lockstep.
- a0cefdf: `wire.RosterCap` — the roster ceiling for per-index frame baselines (1024) is
  now a contract constant in the `wire` package instead of a hand-mirrored
  literal, and ABI.md §3 documents the bound. The Go guest SDK's internal
  `rosterCap` adopts it directly; the Rust SDK's `ROSTER_CAP` is asserted equal
  by a new Go cross-check test (`wire.TestRustRosterCapMatchesWire`, which
  parses the Rust source so no Rust toolchain is needed). No encoding or
  behavior change — purely promoting an existing protocol invariant into the
  contract package both sides compile against.

### Patch Changes

- a0cefdf: The repo now ships an MIT `LICENSE` file at the root — `rust/Cargo.toml`
  already declared `license = "MIT"`, but the module itself carried no license
  text, leaving authors without usage terms. Doc consistency fixes ride along:
  GUIDE.md now states the platform player bound explicitly as 1..1024
  (`wire.RosterCap`) in both the Multiplayer and Large rooms sections, and the
  smoke-script section documents that smoke scripts drive at most 8 seats (the
  runner clamps `MinPlayers` to the seat count, so large-room games still pass;
  large-room behavior is covered by `shellcade-kit check`'s budget gates). The
  Rust README notes that the next shellcade-kit release makes `shellcade-kit
play` accept a game directory and run the cargo wasm build itself.

## 2.7.0

### Minor Changes

- 40d8258: Room lifecycle declarations: `GameMeta.Lifecycle` chooses what happens when
  everyone leaves the room.

  - `LifecycleResumable` (default, byte-compat): hibernate on abandonment,
    player-driven resume — today's behavior.
  - `LifecycleEphemeral`: after the abandonment grace the room ends and
    disposes — no snapshot, no Resume-menu entry. Right for casual social
    rooms; the grace still protects against connection blips.
  - `LifecycleResident`: one long-lived room per slug (persistent worlds):
    ticks with zero players, periodic checkpoints, boot auto-restore.
    Granted per slug by the platform — an ungranted declaration behaves as
    resumable. Cannot combine with `MinPlayers > 1` (validated at meta
    encode, like all trailer fields).

  Carried as a trailing presence-guarded byte after the large-room meta
  section (ABI major stays 2; older payloads decode as resumable, older hosts
  ignore the byte). Rust crate mirrors the field, validation, and goldens.
  `GUIDE.md` gains "Choosing a lifecycle".

## 2.6.0

### Minor Changes

- 6df5bea: Large-room callbacks: negotiated roster-epoch ctx encoding, game-declared
  heartbeats, and the Rust mirror of the 1024-player baseline work.

  - **Ctx roster-epoch mode** (`GameMeta.CtxFeatures: kit.CtxFeatRosterEpoch`):
    the host sends the full member list only when the roster changes (with a
    `u32` epoch) and a 6-byte unchanged marker otherwise — removing the
    O(members) encode/decode/copy from every callback (~100KB per input at
    1000 players, the dominant large-room input cost and the residual
    `-gc=leaking` leak). Sentinel member-counts `0xFFFE`/`0xFFFF` (ABI.md
    §4.1); legacy guests stay byte-identical; ABI major stays 2. Both SDKs
    decode the sentinels and keep an epoch-aware roster cache (zero member
    allocations on the unchanged form); the legacy byte-skim cache is retained
    for pre-feature hosts.
  - **`GameMeta.HeartbeatMS`**: declare your wake cadence (0 = default;
    validated 20..1000 at meta encode). Host precedence: admin
    `host.heartbeat_ms` > declaration > 50ms default. `shellcade-kit play`
    honors the declaration locally.
  - **Rust crate parity**: mirrors both meta fields + the sentinel decode +
    the epoch roster cache (`Rc`-shared members), and ALSO picks up the
    v2.5.0 Go-only baseline work it missed: `ROSTER_CAP` 16 → 1024 with
    lazily-allocated per-slot baselines and allocated-only broadcast
    reconcile. Golden vectors pin the Go/Rust meta encodings byte-identical.
  - New trailing meta section (`u32 ctxFeatures` + `u16 heartbeatMS`) after
    the config-spec section, presence-guarded both directions; `GUIDE.md`
    gains the large-room authoring section (heartbeat + feature declaration +
    render-on-change dirty tracking).

## 2.5.0

### Minor Changes

- ae1e169: Large-room scale: the guest SDK now supports rooms of up to 1024 players.

  - **Per-index frame baselines raised 16 → 1024, allocated lazily.** The SDK
    used to silently drop `Send` for a roster index ≥ 16; the per-slot baseline
    table is now sized for 1024 consumers but each ~45 KiB slot is allocated on
    first commit, so guest linear memory tracks the ACTIVE roster, not the cap.
    A broadcast (`Identical`) reconciles only allocated slots; a never-sent-to
    slot recovers via its first send opening with a keyframe (unconditionally
    accepted) — the same path as a roster change. No wire/ABI change.
  - **Cross-callback roster cache.** The host re-sends the full member list in
    every callback payload, but rosters change only on join/leave/index-shift.
    The SDK now skims the member section's raw bytes (new additive
    `wire.(*Rd).SkipStr`) and compares them to the previous callback's: on a
    match the previously decoded `[]Player` is reused with zero allocation;
    only a real roster change re-decodes. This replaces the roster fingerprint
    hash (the byte compare is strictly stronger) and removes an O(members)
    allocation from EVERY callback — which, under `-gc=leaking`, leaked the
    roster at callback rate and OOM'd long-lived large rooms (~100 KB/callback
    at 1000 players). Lifetime contract: the slice from `Room.Members()` is
    valid for the duration of the callback; copy `Player` values you retain
    (long-lived state should be keyed by `AccountID`, as before).

## 2.4.0

### Minor Changes

- 80b4960: docs: export the getting-started guide via the new `docs` package. A new
  author-facing `docs/get-started.md` (terminal-first, hard-wrapped at <=76
  columns) covers the quickstart (`shellcade-kit new` / `check` / `play`),
  publishing to the public games catalog, and linking GitHub over SSH. The
  new `docs` package embeds it as `docs.GetStarted` so the shellcade arcade
  can render it directly in its "Add your own game" screen.

## 2.3.0

### Minor Changes

- eea2ca7: Declared config key specs: `GameMeta.Config []ConfigKeySpec` (Go) /
  `Meta.config: &[ConfigKeySpec]` (Rust) lets a game declare the admin-settable
  config keys it reads — key, title, description, value type
  (`text`/`number`/`bool`/`json`), the default used when unset, and (json keys)
  an optional JSON Schema — so the arcade's admin tools can render typed get/edit
  forms instead of a blind key/value prompt. Carried as a trailing
  presence-guarded section of the packed Meta (ABI.md §4.2): old payloads decode
  with no specs, old hosts ignore the trailing bytes — ABI major stays 2.
  `wire.ValidateConfigSpecs` is the shared authoring rule set (unique non-empty
  keys, no reserved `host.` prefix, schema only on json keys and well-formed),
  enforced at `meta()` encode time by both SDKs; the Rust encoding is pinned
  byte-identical to Go by a golden vector.

## 2.2.0

### Minor Changes

- 36477ba: Rust SDK: new `shellcade-kit` crate at `rust/` — author a shellcade game in
  Rust as a `Game`/`Handler` impl plus `shellcade_game!(MyGame)`, with the SDK
  owning the entire ABI v2 frame-delta discipline (per-player baselines,
  host-authoritative epochs, keyframe bootstrap/roster invalidation, and the
  in-call keyframe retry on epoch rejection). Tolerant input decoding delivers a
  typed `Input::Char/Key` sum or drops the event; game crates build with
  `#![forbid(unsafe_code)]`. Ships as a git dependency pinned to the kit release
  tag (`shellcade-kit new --rust` scaffolds it). The byte-verified RUN-LIST
  delta encoder moved from `crossverify` into the crate; crossverify now
  path-depends on it as the golden-vector harness, still byte-identical to the
  Go reference encoder.

## 2.1.1

### Patch Changes

- 09e339f: Fix Windows builds: the dev runner's terminal-resize watch (SIGWINCH) is now
  behind Unix build tags with a no-op Windows fallback, so importing the kit
  module compiles on GOOS=windows. The runner works on Windows minus live
  re-letterboxing on resize (no SIGWINCH equivalent exists there).

## 2.1.0

### Minor Changes

- 20fa285: Smoke scripts: every game can ship a `smoke.yaml` — a small deterministic
  script (seed, seats, steps) that drives the game on a virtual clock and dumps
  named 80×24 screens. New public `smoke` package owns the contract
  (`Parse`/`Run`/`RenderANSI`/`RenderText`/`WriteShots`); the native dev runner
  gains `-smoke <file> [-smoke-out <dir>]` so `go run . -smoke smoke.yaml`
  writes shots with no TinyGo involved; `shellcade-kit smoke` runs the same
  script against the built wasm and renders through the same encoder, so the
  two paths emit byte-identical files. GUIDE.md gains a "Smoke scripts" section
  with the schema and authoring guidance.

## 2.0.2

### Patch Changes

- b21dfdc: shellcade-kit (ride-along): `new` scaffolds the `github.com/shellcade/kit/v2`
  module path — the 2.0.1 binary still templated the v1 path, so freshly
  scaffolded games could not resolve the SDK. Kit module itself is unchanged;
  this patch exists so the fixed binary can ship under the lockstep rule.

## 2.0.1

### Patch Changes

- 19ed64a: SDK: a rejected delta is immediately re-sent as a keyframe within the same
  `Send`/`Identical` call, so no render is ever lost to an epoch rejection. Without
  the retry, the first post-restore frame per consumer slot silently vanished
  (stale screen until the next render) and restored rooms failed byte-identical
  hibernation conformance — per-player games drop one frame per seat, exceeding
  any single-drop tolerance. ABI.md §4.6/§4.7 now state the retry as the normative
  guest behavior (hibernation conformance compares frame-for-frame, no tolerance).

## 2.0.0

### Major Changes

- 2394355: ABI v2: frame-delta container + 24-byte grapheme cell.

  This is a **major** ABI break. Rebuild every game against `kit/v2`; the host
  refuses major-1 artifacts. Single-code-point games need **zero source changes**
  — the diffing is transparent — but the module path is now
  `github.com/shellcade/kit/v2`.

  - **Frame is now a delta container.** `Room.Send`/`Room.Identical` ship a
    variable-length run-coalesced delta of the changed cells instead of the whole
    46KB grid, transparently behind the unchanged `Room` interface. A full frame
    (first send, recovery, repaint) is the container's self-describing **keyframe**
    form (46093 bytes). The steady state is allocation-free under `-gc=leaking`
    (reused per-consumer baseline + delta scratch). The host is the baseline
    authority: `send`/`identical` now **return a `u32` epoch** the guest mirrors,
    so a rejected delta self-heals to a keyframe with no guest hibernation or
    roster logic.

  - **24-byte grapheme cell + `SetGrapheme`.** Cells now carry up to three code
    points (`Cell.Cp2`/`Cp3`), and `(*Frame).SetGrapheme` / `SetGraphemeWide` draw
    multi-code-point emoji — VS16 (`❤️`), skin-tone (`👍🏽`), keycaps (`1️⃣`). Clusters
    over three code points (family ZWJ emoji) are refused, not truncated. The wire
    encode path enforces a **canonical-zero rule** (unused cp slots and pad are
    zero) so cell equality is a `memcmp` — load-bearing for delta determinism and
    hibernation byte-identity. `SetRune`/`Set`/`Text`/`SetWide` are unchanged and
    leave the new slots empty, so existing games compile and render identically.

  - **Forward-compatible evolution rules (ABI.md §5).** Guests ignore unknown
    input `kind`/`key` and trailing payload bytes; renderers ignore unknown `attr`
    bits; unassigned header `flags` bits and cell `pad` MUST be zero and are
    rejected until a future minor assigns them; the epoch return reserves its upper
    32 bits for a future host→guest channel. These turn input growth, new text
    attributes, and flag-gated features into minors instead of majors.

  - **Docs.** ABI.md is rewritten for v2 (the delta container §4.5, the
    canonical-zero rule, the epoch/baseline-authority contract, the hand-rolled-
    guest envelope, and the evolution rules), and GUIDE.md gains a "Grapheme
    glyphs" section.

### Minor Changes

- 7370858: Remove the reserved `profile_get` host function from the ABI (`wire.FnProfileGet`
  and its ABI.md table row). It was never implemented host-side, never exposed by
  the SDK, and no guest could usefully call it ("may return 0"). A future profile
  surface would arrive as a new additive host function with a defined payload
  encoding.

## 0.6.0

### Minor Changes

- 83ae78d: Dev-runner polish and a wide-glyph helper.

  - **Deterministic native clock.** `go run . -seed N` now drives a virtual room
    clock: it starts at a fixed seed-derived epoch and advances exactly one
    heartbeat per `OnWake`, so time-derived behavior reproduces frame for frame.
    Without `-seed`, `r.Now()` stays the wall clock.
  - **`Frame.SetWide`.** A helper for double-width glyphs (CJK, emoji): it writes
    the rune plus its continuation cell so the glyph owns both columns, and
    refuses cleanly when it can't fit (out of bounds or the right edge). `Text`
    remains one-rune-one-column.
  - **Robust native input.** The dev runner now tolerates escape sequences split
    across reads, paste bursts, terminal resize (SIGWINCH re-letterboxes), and
    undersized terminals (a "too small" notice that resumes on resize) — across
    both single-seat and `-seats` hot-seat play.
  - **Docs.** A line-referenced wake-idiom cookbook in `examples/pokies/README.md`
    and GUIDE.md updates for the native clock, wide glyphs, and resize handling.

## 0.5.0

- One author tool: scaffolding moved into `shellcade-kit new` (the same binary
  that verifies and plays artifacts — download from this repo's Releases).
  `cmd/kit` removed; the module tag remains the release for the SDK itself.

## 0.4.0 — playtest feedback round 1 (asteroids)

- **keyhold** package: held-key state derived from terminal auto-repeat —
  hold-to-thrust/fire for action games (terminals have no key-up; see GUIDE).
- **kittest** package: in-memory Room/Services test double (virtual clock,
  seeded RNG, recorded frames/posts) for unit-testing game logic.
- **Frame.Clear()**: reuse one frame per render — allocation-free steady state.
- pokies example now **Posts** peak scores: the worked answer to "how does my
  score reach the leaderboard" (Post/End feed boards; KV never does).
- GUIDE: action-games section (held keys, raw input, ~2:1 cell aspect,
  reserved keys), scores & leaderboards (End vs Post semantics), full Room
  reference table, frame-reuse idiom, native wall-clock determinism caveat,
  TinyGo-is-the-artifact-toolchain note. ABI.md: sum/max values are base-10
  ASCII int64.

## 0.3.1

- `kit new` scaffolds pin the CLI's own module version (via build info), fixing
  scaffolds that pointed at a pre-rename version with the old module path.
- Deleted the pre-rename tags (v0.1.0, v0.2.0) whose go.mod declared
  `github.com/shellcade/gamekit`.

## 0.3.0

- Repo renamed `gamekit` → **`kit`** and flipped public; module path is now
  `github.com/shellcade/kit`, the root package is `kit`, and the author CLI is
  `kit` (`kit new <name>`). (This release was cut manually during the rename;
  versions resume via changesets from here.)
- Restructured as a proper Go repo: root facade over `internal/game`, `wire/`
  as the ABI's code form, `cmd/kit`.
- Added `ABI.md` (normative contract) and `GUIDE.md` (authoring guide).
- Release pipeline: changesets (versions/changelogs) + GoReleaser (CLI
  binaries on `vX.Y.Z` tags).
