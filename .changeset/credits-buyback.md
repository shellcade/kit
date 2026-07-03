---
"kit": minor
---

Add the `credits_buyback` host function (wire revision 8) for casino-kind games: a broke player can trigger a mid-session rebuy without leaving the game. `Credits.Buyback(p) (int64, error)` returns the new balance (symmetric to `Balance`); the host gates it (broke-only floor + a per-day rebuy limit it owns) and makes the credited amount wagerable in the current seat. Additive — existing games are unaffected. The guest SDK (Go + Rust), the host `sdk.CreditsService`, the `memsvc`/`kittest`/dev-runner doubles, and `ABI.md` all gain the method.
