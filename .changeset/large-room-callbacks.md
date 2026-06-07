---
"kit": minor
---

Large-room callbacks: negotiated roster-epoch ctx encoding, game-declared
heartbeats, and the Rust mirror of the 1024-player baseline work.

- **Ctx roster-epoch mode** (`GameMeta.CtxFeatures: kit.CtxFeatRosterEpoch`):
  the host sends the full member list only when the roster changes (with a
  `u32` epoch) and a 6-byte unchanged marker otherwise — removing the
  O(members) encode/decode/copy from every callback (~100KB per input at
  1000 players, the dominant large-room input cost and the residual
  `-gc=leaking` leak). Sentinel member-counts `0xFFFE`/`0xFFFF` (ABI.md
  §4.1); legacy guests stay byte-identical; ABI major stays 2. Both SDKs
  decode the sentinels and keep an epoch-aware roster cache (zero member
  allocations on the unchanged form); the legacy byte-skim cache is retained
  for pre-feature hosts.
- **`GameMeta.HeartbeatMS`**: declare your wake cadence (0 = default;
  validated 20..1000 at meta encode). Host precedence: admin
  `host.heartbeat_ms` > declaration > 50ms default. `shellcade-kit play`
  honors the declaration locally.
- **Rust crate parity**: mirrors both meta fields + the sentinel decode +
  the epoch roster cache (`Rc`-shared members), and ALSO picks up the
  v2.5.0 Go-only baseline work it missed: `ROSTER_CAP` 16 → 1024 with
  lazily-allocated per-slot baselines and allocated-only broadcast
  reconcile. Golden vectors pin the Go/Rust meta encodings byte-identical.
- New trailing meta section (`u32 ctxFeatures` + `u16 heartbeatMS`) after
  the config-spec section, presence-guarded both directions; `GUIDE.md`
  gains the large-room authoring section (heartbeat + feature declaration +
  render-on-change dirty tracking).
