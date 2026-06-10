---
"kit": minor
---

`kittest.Room` gains an opt-in `KVUnavailable` chaos knob that replays the
production host's KV degradation exactly: while set, `Get` reports every key
as missing (`nil, false, nil`) and `Set`/`Delete` return `nil` without
persisting — the ABI has no error channel, so a real store outage never
surfaces a Go error. This lets authors test the read-absent-reinit hazard
(the natural `Get → missing → initialize → Set` wallet pattern silently
resets saved state during a store blip), previously impossible to simulate
because the double's KV always succeeded. GUIDE.md's Durable state section
now documents the degradation semantics and the conservative-missing-read
guidance, and `ExampleRoom_kvUnavailable` demonstrates the failing pattern.
