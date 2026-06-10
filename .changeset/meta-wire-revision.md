---
"kit": minor
---

Wire-revision provenance: the packed Meta gains a trailing presence-guarded
`u16 wireRevision` — a single monotonic counter of wire-visible minor
additions (`wire.Revision`, currently 4; ledger in its docs and ABI.md §4.2),
stamped automatically by both the Go and Rust guest SDKs and never set by
the author. Old hosts ignore the bytes; artifacts built with older kits
decode as revision 0 (unknown). This gives the deploy-order rule (ABI.md §5)
its mechanical anchor: a host can now warn on or refuse artifacts declaring
a revision above the one it implements instead of loading them blind, and
record per-artifact contract provenance. Pure additive trailer following the
established pattern — ABI major stays 2. The Rust SDK's `WIRE_REVISION` is
pinned to `wire.Revision` by a new Go cross-check test
(`wire.TestRustWireRevisionMatchesWire`), and the Go/Rust meta goldens are
updated in lockstep.
