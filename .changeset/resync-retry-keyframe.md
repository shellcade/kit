---
"kit": patch
---

SDK: a rejected delta is immediately re-sent as a keyframe within the same
`Send`/`Identical` call, so no render is ever lost to an epoch rejection. Without
the retry, the first post-restore frame per consumer slot silently vanished
(stale screen until the next render) and restored rooms failed byte-identical
hibernation conformance — per-player games drop one frame per seat, exceeding
any single-drop tolerance. ABI.md §4.6/§4.7 now state the retry as the normative
guest behavior (hibernation conformance compares frame-for-frame, no tolerance).
