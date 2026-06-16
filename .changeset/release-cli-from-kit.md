---
"kit": patch
---

The `shellcade-kit` CLI binary is now built and released from this repository.

On each `vX.Y.Z` tag, GoReleaser builds `./cmd/shellcade-kit`, attaches the
cross-platform archives + checksums to that release, and publishes the Homebrew
cask (`brew install shellcade/tap/shellcade-kit`). The published binary embeds
the same kit version it ships under. No behavior change for game authors.
