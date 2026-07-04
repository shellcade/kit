---
"kit": minor
---

Add a `preview` package and a `shellcade-kit preview pack` CLI subcommand for authoring the arcade's games-screen preview loops. Game authors write a `preview/` directory (a `preview.yaml` manifest plus plain-text frame files, 46×7 cells, optional ANSI SGR color) and pack it into a single self-contained `preview.scp` bundle that ships as a release asset. `preview.Pack` validates and compiles the authoring directory; `preview.Load` parses a bundle back into a playable `Animation`. The format enforces the frame-text contract at pack time — SGR-only escapes, no C1/invalid-UTF-8/variation-selector/ZWJ/keycap hazards, 1–64 frames, per-frame durations clamped to 80–10000ms, pad-never-truncate, and a 64 KiB bundle cap — and the arcade re-runs the identical checks on intake. See the new "Preview loops" section in GUIDE.md.
