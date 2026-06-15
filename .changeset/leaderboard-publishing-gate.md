---
"kit": patch
---

shellcade-kit: make the leaderboard requirement an opt-in publishing gate
(`shellcade-kit check --require-leaderboard`) instead of a generic conformance
verdict.

The behavioral "posts on leave" verdict false-failed correct round-based games
(a mid-play leave is recorded at round settlement, not on the leave callback),
and a static "must declare a board" check inside generic conformance
false-fails the minimal test fixtures that declare no board. The kit module
itself is unchanged; this rides a kit patch bump so the shellcade-kit binary
re-releases in lockstep.
