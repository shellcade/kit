---
"kit": patch
---

`shellcade-kit version` now reports the real release version instead of
`(devel)`/`(unknown)`. GoReleaser builds the binary with plain `go build`, so
`debug.ReadBuildInfo().Main.Version` was empty for the released artifact;
GoReleaser now stamps the tag into `main.version` via `-ldflags`, making the
"binary embeds the same kit version it ships under" claim true. In-tree
`go build`/`go run` is unchanged (still falls back to build info). No behavior
change for game authors.
