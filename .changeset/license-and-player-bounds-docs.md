---
"kit": patch
---

The repo now ships an MIT `LICENSE` file at the root — `rust/Cargo.toml`
already declared `license = "MIT"`, but the module itself carried no license
text, leaving authors without usage terms. Doc consistency fixes ride along:
GUIDE.md now states the platform player bound explicitly as 1..1024
(`wire.RosterCap`) in both the Multiplayer and Large rooms sections, and the
smoke-script section documents that smoke scripts drive at most 8 seats (the
runner clamps `MinPlayers` to the seat count, so large-room games still pass;
large-room behavior is covered by `shellcade-kit check`'s budget gates). The
Rust README notes that the next shellcade-kit release makes `shellcade-kit
play` accept a game directory and run the cargo wasm build itself.
