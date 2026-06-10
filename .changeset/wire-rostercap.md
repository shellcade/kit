---
"kit": minor
---

`wire.RosterCap` — the roster ceiling for per-index frame baselines (1024) is
now a contract constant in the `wire` package instead of a hand-mirrored
literal, and ABI.md §3 documents the bound. The Go guest SDK's internal
`rosterCap` adopts it directly; the Rust SDK's `ROSTER_CAP` is asserted equal
by a new Go cross-check test (`wire.TestRustRosterCapMatchesWire`, which
parses the Rust source so no Rust toolchain is needed). No encoding or
behavior change — purely promoting an existing protocol invariant into the
contract package both sides compile against.
