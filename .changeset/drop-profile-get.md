---
"kit": minor
---

Remove the reserved `profile_get` host function from the ABI (`wire.FnProfileGet`
and its ABI.md table row). It was never implemented host-side, never exposed by
the SDK, and no guest could usefully call it ("may return 0"). A future profile
surface would arrive as a new additive host function with a defined payload
encoding.
