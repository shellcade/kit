---
"kit": major
---

ABI v2: frame-delta container + 24-byte grapheme cell.

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
