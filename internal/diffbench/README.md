# diffbench — frame-delta wire encoding benchmarks

Real, reproducible benchmarks of candidate **frame-delta** wire encodings for
the shellcade guest→host frame channel, against the current full-frame
baseline. Answers: *how much wire bandwidth and guest CPU does a delta encoding
save, at what change-density does it stop paying, and is a full-frame fallback
needed?*

A `Frame` is a fixed 24×80 grid; the packed wire payload is `24*80*16 = 30720`
bytes (ABI.md §4.3). The pre-v2 baseline shipped the full 30720 B on **every**
`send`/`identical`.

In **ABI v2** the frame-delta container *is* the frame path: every
`send`/`identical` carries a delta with the normative **9-byte header** —
`u8 flags` (bit0 = keyframe), `u32 epoch`, `u16 runCount`, `u16 reserved = 0` —
followed by `runCount` runs of `{u16 startIndex, u16 runLen, runLen×16 B cells}`.
The **keyframe form** (flags bit0 + one run covering all 1920 cells) is the
bootstrap/full-frame mechanism and the worst-case fallback at **30733 B**
(`9 + 4 + 30720`). The byte counts below are exactly what a production v2 guest
puts on the wire.

## What is measured

Per *scenario × encoding*: mean **bytes/send**, encode **ns/op** (per
sequence; divide by the reported `frames` for per-frame), and **allocs/op**
(must be 0 — the SDK steady state is allocation-free under TinyGo's leaking GC).

### Encodings (`encode.go`)

- **FULL-baseline** — wire floor: `copy` the 30720 B packed frame.
- **FULL-pack** — *faithful* current baseline: compose all 1920 cells via the
  per-cell pack `internal/game/codec.go` does on every send.
- **CELL-LIST** — per changed cell: `u16 index + 16 B cell` (18 B/cell).
- **DIRTY-ROWS** — `u32` row bitmap + a full 1280 B packed row per dirty row.
- **RUN-LIST** — the **v2 normative container**: the 9-byte header
  (`u8 flags, u32 epoch, u16 runCount, u16 reserved`) + runs of consecutive
  changed cells `{u16 start, u16 len, len×16 B cells}`. A `runCount==0` payload
  (the header alone, 9 B) is the legal "no change" delta.
- **SKIP-IDENTICAL** — compare-only; ship nothing when the frame is unchanged
  (measures the ~30 KB equality compare), else fall back to full. (Pre-v2 model;
  in v2 a no-change frame is the 9-byte `runCount==0` delta, not a 0-byte skip.)
- **RUN+KEYFRAME-fallback** — RUN-LIST that degrades to the **keyframe form**
  (9-byte header with bit0 + one run of all 1920 cells + the full grid) when the
  delta would meet or exceed it. Caps the worst case at **30733 B** — the v2
  production encoder (run-list in the steady state, self-contained keyframe on
  the full-change cliff).

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
static-idle, cursor-blink, scroll-row, half-screen, and the **worst case**
(`synth-worstcase-fullchange` — every cell differs frame-to-frame).

## Results

Mean **bytes/send** (baseline full frame = 30720 B), and **ns/frame** for the
production v2 encoder (`RUN+KEYFRAME-fallback`). Apple M4 Pro, native Go,
`-benchtime=300ms`. `(Δ)` is the scenario's mean changed-cell count. All hot-path
encoders are 0 allocs/op.

| scenario (frames, Δ)            | FULL-baseline | CELL-LIST | DIRTY-ROWS | **RUN-LIST** | SKIP-IDENT. | **RUN+KEYFRAME** | ns/frame (RUN+KF) |
|---------------------------------|--------------:|----------:|-----------:|-------------:|------------:|-----------------:|------------------:|
| tic-tac-toe (13, 16)            |         30720 |       289 |       2564 |      **277** |       16542 |          **277** |              2826 |
| **chess (96, 151)** 🟢          |     **30720** |  **2721** |  **18044** |   **2664**   |   **30400** |         **2664** |          **3105** |
| blackjack (901, 28)             |         30720 |       511 |       2990 |      **478** |       30720 |          **478** |              2936 |
| pokies (277, 1)                 |         30720 |        27 |        434 |       **34** |        2773 |           **34** |              2818 |
| shellracer (796, 2)             |         30720 |        37 |        667 |       **43** |       15399 |           **43** |              2882 |
| synth-static-idle (12, 2)       |         30720 |        32 |        111 |       **37** |        2560 |           **37** |              2787 |
| synth-cursor-blink (16, 2)      |         30720 |        37 |       1284 |       **45** |       30720 |           **45** |              2791 |
| synth-scroll-row (20, 80)       |         30720 |      1433 |       1348 |     **1288** |       30720 |         **1288** |              2773 |
| synth-half-screen (8, 960)      |         30720 |     17282 |      15364 |    **15373** |       30720 |        **15373** |              2384 |
| synth-worstcase (5, 1920)       |         30720 |     34562 |      30724 |    **30733** |       30720 |        **30733** |              2192 |

The **chess** row (🟢) is the new real capture: a turn-based duel with a live
blitz clock, so even idle wakes redraw the clock readout — `RUN-LIST` ships
**2664 B/send mean (9% of baseline)**, an ~11.5× wire reduction, at ~3.1 µs of
guest encode per frame and 0 allocs.

### Worst case (full-change, every cell differs)

| encoding              | B/send | × baseline |
|-----------------------|-------:|-----------:|
| FULL-baseline         |  30720 |       1.0× |
| DIRTY-ROWS            |  30724 |       1.0× |
| **RUN-LIST**          |  30733 |       1.0× |
| **RUN+KEYFRAME-fb**   |  30733 |       1.0× |
| CELL-LIST             |  34562 |       1.1× |

The v2 worst case is the **keyframe form = 30733 B** (`9`-byte header + a `4`-byte
run header covering all 1920 cells + the `30720`-byte grid): a flat +13 B over
the old full frame, and the cliff is bounded — `RUN+KEYFRAME-fallback` never
exceeds it. CELL-LIST is the only encoding that blows past the frame size.

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
