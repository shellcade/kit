---
"kit": patch
---

Fix casino games showing a 0 balance and refusing wagers. The guest runtime built a fresh `room` every callback but created the game's `Handler` (and its stored `Services`) only once at `OnStart`, so `svc.Credits` was permanently bound to the *start* callback's roster. `creditsSvc` resolves a player's host-side index against that roster's `ctx.members`, which is empty for a solo game (the player joins after start) — so every `Balance`/`Wager`/`Settle` was denied at the guest facade (`ErrCreditsDenied`) and never reached the host. The room is now a single persistent instance refreshed in place each callback, so the game's stored `Services` always resolve against the live roster. (The `kittest`/native/memsvc doubles resolve by account id, not roster index, which is why unit tests and conformance did not catch this — it only manifested on the real wasm ABI path.) Casino games must be rebuilt against this release to pick up the fix.
