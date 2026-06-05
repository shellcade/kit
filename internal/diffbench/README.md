# diffbench — frame-delta wire encoding benchmarks

Real, reproducible benchmarks of candidate **frame-delta** wire encodings for
the shellcade guest→host frame channel, against the current full-frame
baseline. Answers: *how much wire bandwidth and guest CPU does a delta encoding
save, at what change-density does it stop paying, and is a full-frame fallback
needed?*

A `Frame` is a fixed 24×80 grid; the packed wire payload is `24*80*16 = 30720`
bytes (ABI.md §4.3). Today **every** `send`/`identical` ships the full 30720 B.

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
- **RUN-LIST** — runs of consecutive changed cells: `u16 start + u16 len + cells`.
- **SKIP-IDENTICAL** — compare-only; ship nothing when the frame is unchanged
  (measures the ~30 KB equality compare), else fall back to full.
- **RUN+FULL-fallback** — RUN-LIST with a 1-byte tag that ships a full frame
  when the delta would exceed the frame size (caps the worst case at 30721 B).

`TestEncodersLossless` round-trips every encoding (`decode.go`) against every
scenario: a frame-diff that loses one byte is disqualified (the hibernation /
conformance bar is byte-identical frames).

## Scenarios

**Real** (captured from catalog games via `kittest`, scripted inputs/wakes,
every sent frame recorded — see the `diffcapture_test.go` harnesses in the
`games` repo): `tic-tac-toe` (sparse/turn-based), `blackjack`
(multiplayer + animated deal/settle), `pokies` (reel-spin animation),
`shellracer` (per-keystroke typing redraw). Stored compactly+losslessly in
`testdata/*.fseq` (each frame = its changed cells vs the previous; the loader
reconstructs exact full frames).

**Synthetic** (labelled `synth-*`, mirroring games-survey patterns):
static-idle, cursor-blink, scroll-row, half-screen, and the **worst case**
(`synth-worstcase-fullchange` — every cell differs frame-to-frame).

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
