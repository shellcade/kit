---
"github.com/shellcade/kit/v2": minor
---

Casino games: the game-kind meta section, credits host functions, and CtxFeatCredits (wire revision 7)

- `GameMeta.Kind` (`GameKindGame` | `GameKindCasino`) classifies a game for
  the platform economy, with `GameMeta.MaxPayoutMultiplier` declaring a
  casino game's per-stake payout ceiling — a new presence-guarded trailing
  meta section (absent = game, so every existing artifact keeps its meaning).
- Three new host functions — `credits_balance`, `credits_wager`,
  `credits_settle` — give casino-kind guests an account-wide, host-owned
  wallet: atomic escrow wagers, gross (stake-inclusive) settlement clamped to
  stake × the declared multiplier, typed refusals
  (`ErrInsufficientCredits`, `ErrEconomyDisabled`, `ErrCreditsDenied`,
  `ErrCreditsUnavailable`). Game-kind guests are rejected host-side.
- `CtxFeatCredits` (bit 2) declares that an artifact wagers
  (declaration-only; no encoding change).
- Go guest SDK: `Services.Credits`; Rust guest SDK: `Room::credits_balance` /
  `credits_wager` / `credits_settle`, `Meta::kind` +
  `Meta::max_payout_multiplier`, `CTX_FEAT_CREDITS`, `CreditsError`.
- `kittest.Room` gains an in-memory wallet double (`Credits`,
  `CreditsStakes`, `CreditsDisabled`); the native dev runner and `memsvc`
  (behind `shellcade-kit check`/`play`) implement the same semantics.
- Wire revision 6 → 7; ABI.md §3/§4.2 and GUIDE.md document the contract.
