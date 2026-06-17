---
"kit": minor
---

feat(cli): lint game source for wide-glyph width-contract violations

`shellcade-kit lint-width <path>...` parses Go game source and flags every
wide-glyph writer call — `(*Frame).SetWide` / `(*Frame).SetGraphemeWide` — whose
base code point is a determinable literal but is NOT East-Asian-Width Wide (W)
or Fullwidth (F). Such a base reserves two terminal columns for a glyph the
terminal advances by one, desyncing every column to its right — the bug that
corrupted the pokies reels in production (a keycap base `U+0037`, EAW Neutral,
fed to a wide writer). Each violation is reported with `file:line` and the
offending code point, and the command exits non-zero on any violation, so it is
a one-command merge gate.

`shellcade-kit check <gamedir>` now runs this lint over the Go source before the
build + conformance run, since a width-contract desync never faults and so
cannot be observed by conformance alone.

The EAW judgement embeds the Wide/Fullwidth ranges of the Unicode Character
Database `EastAsianWidth.txt`; the public module stays dependency-free. Additive
only: the guest SDK and the ABI are unchanged.
