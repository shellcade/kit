---
"kit": minor
---

Dev-runner polish and a wide-glyph helper.

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
