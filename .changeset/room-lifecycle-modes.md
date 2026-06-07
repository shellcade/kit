---
"kit": minor
---

Room lifecycle declarations: `GameMeta.Lifecycle` chooses what happens when
everyone leaves the room.

- `LifecycleResumable` (default, byte-compat): hibernate on abandonment,
  player-driven resume — today's behavior.
- `LifecycleEphemeral`: after the abandonment grace the room ends and
  disposes — no snapshot, no Resume-menu entry. Right for casual social
  rooms; the grace still protects against connection blips.
- `LifecycleResident`: one long-lived room per slug (persistent worlds):
  ticks with zero players, periodic checkpoints, boot auto-restore.
  Granted per slug by the platform — an ungranted declaration behaves as
  resumable. Cannot combine with `MinPlayers > 1` (validated at meta
  encode, like all trailer fields).

Carried as a trailing presence-guarded byte after the large-room meta
section (ABI major stays 2; older payloads decode as resumable, older hosts
ignore the byte). Rust crate mirrors the field, validation, and goldens.
`GUIDE.md` gains "Choosing a lifecycle".
