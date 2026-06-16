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
  buffers, freed Extism memory) — under `-gc=conservative` steady-state
  allocations are GC pressure inside the callback deadline.

## Docs live here, and they are the product

Public documentation in this repo should be rich and author-focused:
`README.md` (orientation + quickstart), `GUIDE.md` (how to develop a game:
the dev loop, wake idioms, services, multiplayer testing), `ABI.md` (the
normative contract). Improving these is always in scope.

## Releasing

Versions/changelogs are driven by **changesets** — THIS repo is the single
version authority. Every user-visible change adds a `.changeset/*.md` (run
`npx changeset`); merging to main lets the changesets action open a Version
Packages PR; merging THAT pushes the `vX.Y.Z` tag, and the tag IS the module
release. Never hand-edit CHANGELOG.md or push tags manually.

The `vX.Y.Z` tag also drives the `shellcade-kit` **CLI binary** release: the
`release-cli.yml` workflow runs GoReleaser (`.goreleaser.yaml`) to build
`./cmd/shellcade-kit`, attach the cross-platform archives + checksums to that
release, publish the Homebrew cask (`brew install shellcade/tap/shellcade-kit`),
and then fire a `kit-released` `repository_dispatch` to the consumer repos so
they can re-pin to the new toolchain. The binary therefore embeds the same kit
version it ships under — no separate version authority, and a CLI-only fix
still rides a kit patch bump.

## Build & test

```sh
go test ./...                                  # incl. wire round-trip/fuzz
go build ./... && go vet ./...

# end-to-end author journey (CI mirrors this): scaffold, build, verify.
go run ./cmd/shellcade-kit new mygame
cd mygame && go mod tidy && go run .           # native dev runner
tinygo build -opt=1 -no-debug -gc=conservative \
    -o mygame.wasm -target wasip1 -buildmode=c-shared .
```

There are no in-repo example games: the published
[games catalog](https://github.com/shellcade/games) is the example gallery.
