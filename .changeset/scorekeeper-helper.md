---
"kit": minor
---

feat: add `ScoreKeeper` leaderboard helper

A small, timer-free helper that standardises the three ways a game posts to the
leaderboard, replacing the bespoke per-game logic that kept getting the
disconnect/continuous cases wrong:

- `Record(r, p, metric)` — post live per a `Cadence` (`OnImprove` for monotonic
  high-water boards, `OnChange` for live scores).
- `FlushLeave(r, p, status)` — post the player's current metric on disconnect
  (call from `OnLeave`); normally `StatusDNF`.
- `FlushAll(r, status)` — post every tracked player in deterministic AccountID
  order; continuous games call this from `OnWake` so an abandoned, still-ticking
  world keeps recording.
- `PersistBest` / `PersistWallet` — KV resume sugar (MergeMax / MergeSum).

Pure SDK addition over the existing `Room.Post` + KV surface — no wire or ABI
change.
