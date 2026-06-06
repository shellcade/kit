---
"kit": patch
---

shellcade-kit (ride-along): `new` scaffolds the `github.com/shellcade/kit/v2`
module path — the 2.0.1 binary still templated the v1 path, so freshly
scaffolded games could not resolve the SDK. Kit module itself is unchanged;
this patch exists so the fixed binary can ship under the lockstep rule.
