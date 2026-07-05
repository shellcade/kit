---
"kit": patch
---

Fix every unseeded wasm room sharing one deterministic RNG stream. The guest runtime seeds the SDK room PRNG (`r.Rand()` in Go, `rng_u64` in Rust) from the CallContext seed verbatim, but for a room without an operator-set seed the host encoded the raw config zero — so every room of every game drew the identical "random" sequence (identical blackjack shoes across rooms; casino outcomes learnable by replaying a throwaway room and then betting on the known stream). The host now derives a per-room seed from the host CSPRNG when `SeedSet` is false and carries it in the room's config, so the Ctx (and the WASI entropy source, which previously used the guessable room-start timestamp) are seeded per room. Host-side only: existing game binaries pick up the fix without a rebuild. Explicitly seeded runs (`--seed` / `-seed`, conformance, hibernation restore) are unchanged and stay deterministic.
