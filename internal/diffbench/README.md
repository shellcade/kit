# diffbench — frame-delta wire encoding benchmarks

Real, reproducible benchmarks of candidate **frame-delta** wire encodings for
the shellcade guest→host frame channel, against the current full-frame
baseline. Answers: *how much wire bandwidth and guest CPU does a delta encoding
save, at what change-density does it stop paying, and is a full-frame fallback
needed?*

A `Frame` is a fixed 24×80 grid. **ABI v2** cells are **24-byte grapheme
cells** — the diffing makes the wider cell affordable (steady-state deltas stay
tiny; only keyframes/baselines grow). The normative v2 cell layout (canonical-
zero rule: unused `cp` slots and `pad` MUST be zero, so cell equality is a
24-byte `memcmp` and hibernation byte-identical conformance stays well-defined):

```
u32 rune | u32 cp2 | u32 cp3      extra code points for the grapheme cluster
u8 fgSet,fgR,fgG,fgB | u8 bgSet,bgR,bgG,bgB | u8 attr | u8 cont | u16 pad (zero)
```

`cp2`/`cp3` carry VS16, skin-tone modifiers, the keycap `U+20E3`, and ZWJ
pieces (`0` = unused); clusters needing more than three code points (family ZWJ
emoji) are unsupported. A full packed frame is `24*80*24 = 46080` B.

In **ABI v2** the frame-delta container *is* the frame path: every
`send`/`identical` carries a delta with the normative **9-byte header** —
`u8 flags` (bit0 = keyframe), `u32 epoch`, `u16 runCount`, `u16 reserved = 0` —
followed by `runCount` runs of `{u16 startIndex, u16 runLen, runLen×24 B cells}`.
The **keyframe form** (flags bit0 + one run covering all 1920 cells) is the
bootstrap/full-frame mechanism and the worst-case fallback at **46093 B**
(`9 + 4 + 46080`). The byte counts below are exactly what a production v2 guest
puts on the wire.

The committed `.fseq` testdata was captured against the round-1 16-byte cell
(catalog games are single-code-point today). The diffbench loader (`seq.go`,
`widen16to24`) re-packs each capture cell into the 24-byte v2 layout with
`cp2=cp3=pad=0` — an **exact** widening, not a synthetic one — so the
reconstructed frames are the genuine v2 production renders of those games.

## What is measured

Per *scenario × encoding*: mean **bytes/send**, encode **ns/op** (per
sequence; divide by the reported `frames` for per-frame), and **allocs/op**
(must be 0 — the SDK steady state is allocation-free under TinyGo's leaking GC).

### Encodings (`encode.go`)

- **FULL-baseline** — wire floor: `copy` the 46080 B packed frame.
- **FULL-pack** — *faithful* current baseline: compose all 1920 cells via the
  per-cell pack the v2 codec does on every send (three u32 code points + fg/bg +
  attr + cont + zero pad).
- **CELL-LIST** — per changed cell: `u16 index + 24 B cell` (26 B/cell).
- **DIRTY-ROWS** — `u32` row bitmap + a full 1920 B packed row per dirty row.
- **RUN-LIST** — the **v2 normative container**: the 9-byte header
  (`u8 flags, u32 epoch, u16 runCount, u16 reserved`) + runs of consecutive
  changed cells `{u16 start, u16 len, len×24 B cells}`. A `runCount==0` payload
  (the header alone, 9 B) is the legal "no change" delta.
- **SKIP-IDENTICAL** — compare-only; ship nothing when the frame is unchanged
  (measures the ~46 KB equality compare), else fall back to full. (Pre-v2 model;
  in v2 a no-change frame is the 9-byte `runCount==0` delta, not a 0-byte skip.)
- **RUN+KEYFRAME-fallback** — RUN-LIST that degrades to the **keyframe form**
  (9-byte header with bit0 + one run of all 1920 cells + the full grid) when the
  delta would meet or exceed it. Caps the worst case at **46093 B** — the v2
  production encoder (run-list in the steady state, self-contained keyframe on
  the full-change cliff).

The dirty scan's inner cell-equality check is three `uint64` loads per cell
(the 24-byte cell is 8-byte aligned; canonical-zero makes equality a `memcmp`).

`TestEncodersLossless` round-trips every encoding (`decode.go`) against every
scenario: a frame-diff that loses one byte is disqualified (the hibernation /
conformance bar is byte-identical frames). All hot-path encoders are **0
allocs/op** (verified by `-benchmem`).

## Scenarios

**Real** (captured from catalog games via `kittest`, scripted inputs/wakes,
every sent frame recorded — see the `diffcapture_test.go` harnesses in the
`games` repo): `tic-tac-toe` (sparse/turn-based), `chess` (turn-based with a
**live blitz clock** — opening moves driven through the real cursor-navigation
input path, idle clock wakes between moves), `blackjack` (multiplayer + animated
deal/settle), `pokies` (reel-spin animation), `shellracer` (per-keystroke typing
redraw). Stored compactly+losslessly in `testdata/*.fseq` (each frame = its
changed cells vs the previous; the loader reconstructs exact full frames).

**Synthetic** (labelled `synth-*`, mirroring games-survey patterns):
static-idle, cursor-blink, scroll-row, half-screen, the **worst case**
(`synth-worstcase-fullchange` — every cell differs frame-to-frame), and
`synth-grapheme-churn` — a row of **wide emoji grapheme clusters** whose VS16 /
skin-tone modifier / keycap code points mutate every frame, the v2-specific
scenario that exercises `cp2`/`cp3` **actually non-zero** (the single-code-point
catalog captures never touch them; each emoji is a wide glyph: base cell + a
`cont=1` continuation cell).

## Results

Mean **bytes/send** (v2 full-frame floor = **46080 B**; keyframe form =
**46093 B**), and **ns/frame** for the production v2 encoder
(`RUN+KEYFRAME-fallback`). Apple M4 Pro, native Go, `-benchtime=300ms`. `(Δ)` is
the scenario's mean changed-cell count. All hot-path encoders are 0 allocs/op.

| scenario (frames, Δ)            | FULL-baseline | CELL-LIST | DIRTY-ROWS | **RUN-LIST** | SKIP-IDENT. | **RUN+KEYFRAME** | ns/frame (RUN+KF) |
|---------------------------------|--------------:|----------:|-----------:|-------------:|------------:|-----------------:|------------------:|
| tic-tac-toe (13, 16)            |         46080 |       416 |       3844 |      **405** |       24812 |          **405** |              3740 |
| **chess (96, 151)** 🟢          |     **46080** |  **3929** |  **27064** |   **3873**   |   **45600** |         **3873** |          **3991** |
| blackjack (901, 28)             |         46080 |       738 |       4483 |      **705** |       46080 |          **705** |              3806 |
| pokies (277, 1)                 |         46080 |        38 |        649 |       **45** |        4159 |           **45** |              3817 |
| shellracer (796, 2)             |         46080 |        53 |        998 |       **59** |       23098 |           **59** |              3814 |
| synth-static-idle (12, 2)       |         46080 |        45 |        164 |       **50** |        3840 |           **50** |              3660 |
| synth-cursor-blink (16, 2)      |         46080 |        52 |       1924 |       **61** |       46080 |           **61** |              3657 |
| synth-scroll-row (20, 80)       |         46080 |      2069 |       2020 |     **1924** |       46080 |         **1924** |              3603 |
| **synth-grapheme-churn (20, 19)** 🟣 | **46080** | **503** |  **2020**  |   **534**    |   **46080** |         **534**  |          **3735** |
| synth-half-screen (8, 960)      |         46080 |     24962 |      23044 |    **23053** |       46080 |        **23053** |              2817 |
| synth-worstcase (5, 1920)       |         46080 |     49922 |      46084 |    **46093** |       46080 |        **46093** |              2246 |

The **chess** row (🟢) is the real capture (committed `chess.fseq`,
deterministically reproducible from the games-repo kittest harness): a turn-based
duel with a live blitz clock, so even idle wakes redraw the clock readout —
`RUN-LIST` ships **3873 B/send mean (8% of baseline)**, an **~11.9× wire
reduction**, at ~4.0 µs of guest encode per frame and 0 allocs.

The **grapheme-churn** row (🟣) is the v2-only synthetic where `cp2`/`cp3`
carry real code points: 16 wide emoji whose VS16 / skin-tone / keycap members
mutate per frame. Even with three live code points per changed cell, `RUN-LIST`
ships **534 B/send (1% of baseline)** — the wider cell is fully absorbed by the
delta.

### Worst case (full-change, every cell differs)

| encoding              | B/send | × baseline |
|-----------------------|-------:|-----------:|
| FULL-baseline         |  46080 |       1.0× |
| DIRTY-ROWS            |  46084 |       1.0× |
| **RUN-LIST**          |  46093 |       1.0× |
| **RUN+KEYFRAME-fb**   |  46093 |       1.0× |
| CELL-LIST             |  49922 |       1.08× |

The v2 worst case is the **keyframe form = 46093 B** (`9`-byte header + a `4`-byte
run header covering all 1920 cells + the `46080`-byte grid): a flat +13 B over
the full frame, and the cliff is bounded — `RUN+KEYFRAME-fallback` never exceeds
it. CELL-LIST is the only encoding that blows past the frame size (26 B/cell ×
1920 + header).

### 24-byte v2 cells vs round-1 16-byte numbers

Widening the cell from 16 → 24 B (×1.5) feeds straight through to anything that
ships whole cells, but the deltas stay tiny in absolute terms and *shrink* as a
fraction of the frame. The full-frame floor goes 30720 → 46080 B (×1.5) and the
keyframe form 30733 → 46093 B (×1.5, the +13 B framing overhead is unchanged).
Steady-state `RUN-LIST` deltas grow by roughly the cell-bytes ratio on
cell-bound rows and less where per-run/header overhead dominates: chess
2664 → 3873 B (~1.45×), blackjack 478 → 705 (~1.47×), tic-tac-toe 277 → 405
(~1.46×); the sparse single-cell scenarios are dominated by run framing and move
less (pokies 34 → 45, shellracer 43 → 59). Crucially, because the denominator
grew by the *same* 1.5×, the **delta-as-fraction-of-frame is flat or better**:
chess holds at ~8–9% (now 8.4% of 46080 vs 8.7% of 30720), and every real
scenario still ships **<2% of a frame** in the steady state (chess, the densest
real capture, is the only one above 2%, at ~8%). The wider grapheme cell is
"free" precisely because diffing pays only for *changed* cells — the 1.5× lands
on the rare keyframe/baseline, not the common delta. Encode CPU rose ~30% per
frame (three `uint64` compares per cell instead of two, plus 1.5× the bytes
copied into runs): chess ~3.1 → ~4.0 µs/frame, still single-digit microseconds
and 0 allocs.

## Run

```sh
go test -bench . -benchmem ./internal/diffbench/
go test -run TestReport -v   ./internal/diffbench/   # size table
go test -run TestWorstCase -v ./internal/diffbench/  # the cliff
```

NATIVE Go numbers. Under TinyGo/wasm absolute ns differ by a roughly constant
factor (no SIMD memcmp, simpler optimizer); the **byte counts are exact for
wasm too**, and the relative CPU ordering carries over.

## Regenerating testdata

In the `games` worktree (with a `go.mod replace` pointing each game at this kit
worktree):

```sh
go test -run TestCaptureFrameSeq ./...   # in each games/bcook/<game>
```
