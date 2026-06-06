---
"kit": minor
---

Smoke scripts: every game can ship a `smoke.yaml` — a small deterministic
script (seed, seats, steps) that drives the game on a virtual clock and dumps
named 80×24 screens. New public `smoke` package owns the contract
(`Parse`/`Run`/`RenderANSI`/`RenderText`/`WriteShots`); the native dev runner
gains `-smoke <file> [-smoke-out <dir>]` so `go run . -smoke smoke.yaml`
writes shots with no TinyGo involved; `shellcade-kit smoke` runs the same
script against the built wasm and renders through the same encoder, so the
two paths emit byte-identical files. GUIDE.md gains a "Smoke scripts" section
with the schema and authoring guidance.
