# kit

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
