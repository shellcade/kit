# kit

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
