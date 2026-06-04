# kit — repo guide for Claude

This is the **PUBLIC** shellcade game developer kit: the wasm game ABI
(normative `ABI.md` + the `wire` package as its code form), the Go guest SDK,
and example games. Third-party game authors import this module.

## Hard rules

- **NEVER add shellcade-internal material to this repo.** No OpenSpec
  artifacts (`openspec/`, proposal/design/tasks documents), no references to
  shellcade's private packages, infrastructure, deployment, or roadmap. Specs
  and design docs for the platform live in the private repo only. If a change
  here needs design discussion, it happens in the private repo's OpenSpec and
  only the resulting public-facing contract lands here.
- This module MUST import no shellcade private code — it is implementable
  from `ABI.md` alone, and that property is the point.
- The ABI is **versioned and frozen**: `wire` changes that alter encodings or
  semantics are a new ABI version and a module major version. Additive host
  functions / trailing presence-guarded fields are minor versions.
- Frames pass by **pointer** in all SDK surfaces (see ABI.md §6) — never
  by value.
- Keep the steady state allocation-free in guest paths (reused encode
  buffers, freed Extism memory) while the TinyGo GC issue is open.

## Docs live here, and they are the product

Public documentation in this repo should be rich and author-focused:
`README.md` (orientation + quickstart), `GUIDE.md` (how to develop a game:
the dev loop, wake idioms, services, multiplayer testing), `ABI.md` (the
normative contract). Improving these is always in scope.

## Releasing

Versions/changelogs are driven by **changesets**: every user-visible change
adds a `.changeset/*.md` (run `npx changeset`). Merging to main lets the
changesets action open a Version Packages PR; merging THAT pushes the
`vX.Y.Z` tag, and **GoReleaser** builds the `kit` CLI binaries onto the
GitHub release. Never hand-edit CHANGELOG.md or push tags manually.

## Build & test

```sh
go test ./...                                  # incl. wire round-trip/fuzz
go run ./examples/pokies                       # native dev runner
cd examples/pokies && tinygo build -opt=1 -no-debug -gc=leaking \
    -o pokies.wasm -target wasip1 -buildmode=c-shared .
```
