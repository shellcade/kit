# kit

## 0.4.0 — playtest feedback round 1 (asteroids)

- **keyhold** package: held-key state derived from terminal auto-repeat —
  hold-to-thrust/fire for action games (terminals have no key-up; see GUIDE).
- **kittest** package: in-memory Room/Services test double (virtual clock,
  seeded RNG, recorded frames/posts) for unit-testing game logic.
- **Frame.Clear()**: reuse one frame per render — allocation-free steady state.
- pokies example now **Posts** peak scores: the worked answer to "how does my
  score reach the leaderboard" (Post/End feed boards; KV never does).
- GUIDE: action-games section (held keys, raw input, ~2:1 cell aspect,
  reserved keys), scores & leaderboards (End vs Post semantics), full Room
  reference table, frame-reuse idiom, native wall-clock determinism caveat,
  TinyGo-is-the-artifact-toolchain note. ABI.md: sum/max values are base-10
  ASCII int64.

## 0.3.1

- `kit new` scaffolds pin the CLI's own module version (via build info), fixing
  scaffolds that pointed at a pre-rename version with the old module path.
- Deleted the pre-rename tags (v0.1.0, v0.2.0) whose go.mod declared
  `github.com/shellcade/gamekit`.

## 0.3.0

- Repo renamed `gamekit` → **`kit`** and flipped public; module path is now
  `github.com/shellcade/kit`, the root package is `kit`, and the author CLI is
  `kit` (`kit new <name>`). (This release was cut manually during the rename;
  versions resume via changesets from here.)
- Restructured as a proper Go repo: root facade over `internal/game`, `wire/`
  as the ABI's code form, `cmd/kit`.
- Added `ABI.md` (normative contract) and `GUIDE.md` (authoring guide).
- Release pipeline: changesets (versions/changelogs) + GoReleaser (CLI
  binaries on `vX.Y.Z` tags).
