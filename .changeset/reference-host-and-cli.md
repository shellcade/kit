---
"kit": minor
---

feat: the embeddable reference host and the `shellcade-kit` CLI now ship in the module

The host that runs a wasm game against the ABI is now public under `host/`:
`host/gameabi` (the wasm host), `host/sdk` (the room engine + service
interfaces), `host/render` + `host/canvas` (the 80×24 framebuffer and ANSI
render), `host/blobstore` (hibernation snapshots), `host/memsvc` (in-memory
service implementations), and `host/gameabi/conformance` (the game conformance
harness). `cmd/shellcade-kit` (`new` / `check` / `play` / `smoke`) is built from
that reference host, so the CLI and the conformance gate run the exact host a
game runs on — no separate host binary required.

Additive only: the guest SDK and the ABI are unchanged.
