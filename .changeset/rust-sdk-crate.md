---
"kit": minor
---

Rust SDK: new `shellcade-kit` crate at `rust/` — author a shellcade game in
Rust as a `Game`/`Handler` impl plus `shellcade_game!(MyGame)`, with the SDK
owning the entire ABI v2 frame-delta discipline (per-player baselines,
host-authoritative epochs, keyframe bootstrap/roster invalidation, and the
in-call keyframe retry on epoch rejection). Tolerant input decoding delivers a
typed `Input::Char/Key` sum or drops the event; game crates build with
`#![forbid(unsafe_code)]`. Ships as a git dependency pinned to the kit release
tag (`shellcade-kit new --rust` scaffolds it). The byte-verified RUN-LIST
delta encoder moved from `crossverify` into the crate; crossverify now
path-depends on it as the golden-vector harness, still byte-identical to the
Go reference encoder.
