# kit

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
