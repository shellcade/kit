# diff-rs — shellcade ABI v2 RUN-LIST frame-delta encoder, in Rust

A standalone Rust reimplementation of the shellcade **ABI v2** RUN-LIST
frame-delta encoder (24-byte grapheme cells, 9-byte epoch header, keyframe
form), implemented from the v2 cell/container layout alone and **verified
byte-identical** against the Go reference encoder (`kit/internal/diffbench`).

It exists to (a) measure how hard the v2 wire path is to implement in a second
language and (b) act as an executable conformance check on the v2 delta format:
if a frame-diff implementation drops or reorders one byte, the golden-vector
test fails.

This is a **host-native library crate** (not the wasm guest). `cargo test`
builds for the host so the unit + golden-vector tests run with the normal
harness.

## What it encodes (`src/lib.rs`)

The v2 frame-delta container — the frame payload of every `send`/`identical`:

- 9-byte header: `u8 flags` (bit0 = keyframe), `u32 epoch` (host-owned), `u16
  runCount`, `u8 rows = 24`, `u8 cols = 80` (the geometry bytes that replaced
  the former reserved `u16`; the host validates them).
- `runCount` runs of `{u16 startIndex, u16 runLen, runLen × 24-byte cells}`,
  each a maximal span of consecutive *changed* cells.
- `encode_run_list` — the steady-state delta; a `runCount == 0` payload (header
  alone, 9 B) is the legal "no change" delta.
- `encode_keyframe` — the **keyframe form** (flags bit0 + one run over all 1920
  cells + the full 46080-byte grid = 46093 B): bootstrap / full-frame / worst
  case.
- `encode_run_list_or_keyframe` — run-list that degrades to the keyframe form
  when the delta would meet or exceed it (caps the worst case at 46093 B). This
  is what a production guest ships.

Cell equality is a 24-byte compare (the **canonical-zero rule** — unused `cp2`/
`cp3` slots and `pad` are always zero — makes equality a `memcmp`). Encoders
write into a caller-owned reused `dst` and allocate nothing.

## Verification (byte-identical vs Go)

```sh
rustup run stable cargo test --release
```

The `tests/golden.rs` integration test loads committed golden vectors
(`tests/golden/*.dgld`) emitted by the Go reference and asserts this encoder is
byte-identical for every frame of every scenario (real catalog captures +
synthetics, including `synth-grapheme-churn`, which exercises `cp2`/`cp3`
actually non-zero). Current status: **11 scenarios, 2164 frames, 0 mismatches**.

Regenerate / extend the golden vectors from the Go side:

```sh
(cd ../../../.. && cd kit && \
 DIFFBENCH_GOLDEN_DIR=/path/to/out go test -run TestEmitGolden ./internal/diffbench/)
# then point the Rust test at them:
DIFFBENCH_GOLDEN_DIR=/path/to/out rustup run stable cargo test --release --test golden
```

(The `.dgld` format is documented at the top of `tests/golden.rs` and the
emitter `kit/internal/diffbench/golden_test.go`. The input frame is stored as an
encoder-independent changed-cell list so verification is not circular.)

Two CI gates keep the committed vectors a live reference rather than a
historical snapshot: the kit `test` job re-emits them from the current Go
encoder and diffs against `tests/golden` (so a Go byte-output change cannot
silently strand this harness on old bytes), and
`kit/internal/diffbench/parity_test.go` asserts the emitter is byte-identical
to the production `wire.BuildFrameDelta`/`wire.BuildKeyframe` encoders Go
guests actually ship.

The same generated-vector discipline covers the scalar encodings (meta / ctx /
result): `kit/wire/scalar_golden_test.go` emits and freshness-gates
`kit/rust/tests/golden/scalars.txt`, which the SDK crate replays in
`rust/src/wire.rs` (`mod scalar_golden`) — byte-identity for guest-encoded
payloads (meta, result), field + reader-position assertions over the
host-encoded ctx forms.

## Perf sanity

```sh
rustup run stable cargo run --release --example bench
```

A plain warm-loop timer (no criterion) over four change densities, the Rust
counterpart to the Go `BenchmarkEncode` ns/frame. Native, same machine, both
allocation-free; this Rust encoder runs ~1.2–1.4 µs/frame and is ~3× faster than
the Go reference on scan-dominated (sparse) frames because the 24-byte slice
compare lowers to a vectorized `memcmp`.
