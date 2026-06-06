---
"kit": minor
---

Large-room scale: the guest SDK now supports rooms of up to 1024 players.

- **Per-index frame baselines raised 16 → 1024, allocated lazily.** The SDK
  used to silently drop `Send` for a roster index ≥ 16; the per-slot baseline
  table is now sized for 1024 consumers but each ~45 KiB slot is allocated on
  first commit, so guest linear memory tracks the ACTIVE roster, not the cap.
  A broadcast (`Identical`) reconciles only allocated slots; a never-sent-to
  slot recovers via its first send opening with a keyframe (unconditionally
  accepted) — the same path as a roster change. No wire/ABI change.
- **Cross-callback roster cache.** The host re-sends the full member list in
  every callback payload, but rosters change only on join/leave/index-shift.
  The SDK now skims the member section's raw bytes (new additive
  `wire.(*Rd).SkipStr`) and compares them to the previous callback's: on a
  match the previously decoded `[]Player` is reused with zero allocation;
  only a real roster change re-decodes. This replaces the roster fingerprint
  hash (the byte compare is strictly stronger) and removes an O(members)
  allocation from EVERY callback — which, under `-gc=leaking`, leaked the
  roster at callback rate and OOM'd long-lived large rooms (~100 KB/callback
  at 1000 players). Lifetime contract: the slice from `Room.Members()` is
  valid for the duration of the callback; copy `Player` values you retain
  (long-lived state should be keyed by `AccountID`, as before).
