# shellcade-kit (Rust)

The Rust guest SDK for shellcade ABI v2. A game is a `Handler` impl plus one
macro invocation — the SDK owns all the wire plumbing (frame packing, delta
encoding, epochs, keyframe retries), and game code contains no `unsafe`
(the scaffold enforces it with `#![forbid(unsafe_code)]`).

## Quickstart

You need the `shellcade-kit` CLI (see the [kit README](../README.md)) and a
Rust toolchain with the wasm target:

```sh
rustup target add wasm32-wasip1
```

Scaffold, build, verify, play:

```sh
shellcade-kit new --rust mygame
cd mygame
cargo build --release --target wasm32-wasip1
shellcade-kit check target/wasm32-wasip1/release/mygame.wasm
shellcade-kit play  target/wasm32-wasip1/release/mygame.wasm
```

> **Artifact path**: cargo converts dashes in the crate name to underscores —
> `my-game` builds `target/wasm32-wasip1/release/my_game.wasm`. The scaffolded
> README carries your exact path.

> **Coming in the next shellcade-kit release**: `shellcade-kit play` accepts
> a game *directory* and runs the cargo wasm build itself, collapsing the
> iterate loop above to the one command `shellcade-kit play .`. Until then,
> the Rust inner loop is `cargo test` for logic plus the explicit wasm build
> to see your game on screen.

Game logic tests run natively — no wasm runtime needed:

```sh
cargo test
```

## The shape of a game

```rust
#![forbid(unsafe_code)]
use shellcade_kit::prelude::*;

struct MyGame;

impl Game for MyGame {
    fn meta(&self) -> Meta {
        Meta {
            slug: "mygame",                     // == your catalog directory name
            name: "My Game",
            short_description: "One line for the lobby.",
            min_players: 1,
            max_players: 1,
            ..Meta::DEFAULT
        }
    }
    fn new_room(&self, _cfg: &RoomConfig) -> Box<dyn Handler> {
        Box::new(MyRoom { frame: Frame::new() })
    }
}

struct MyRoom {
    frame: Frame,
}

impl Handler for MyRoom {
    fn on_start(&mut self, r: &mut Room) {
        r.set_input_context(InputContext::Nav);
    }
    fn on_input(&mut self, r: &mut Room, _p: Player, input: Input) {
        match input.resolve(InputContext::Nav) {
            Action::Confirm => {
                self.frame.clear();
                self.frame.text(1, 2, "hello, arcade", Style::new(WHITE, ATTR_BOLD));
                r.identical(&self.frame);
            }
            Action::Back => { /* the host handles leaving */ }
            _ => {}
        }
    }
    fn on_wake(&mut self, r: &mut Room) {
        // ~20×/sec host heartbeat. Time comes ONLY from r.now_unix_nanos();
        // randomness ONLY from r.rng_u64()/r.rng_below(n). Render on change.
        let _ = r;
    }
}

shellcade_kit::shellcade_game!(MyGame);
```

The mental model is the Go SDK's, one-for-one — six callbacks, all state in
your room struct, render on change. [GUIDE.md](../GUIDE.md) teaches it; this
table is the dictionary:

| Go | Rust |
|---|---|
| `kit.Handler` (`OnStart`…`OnClose`) | `Handler` (`on_start`…`on_close`; join/leave/wake/close default to no-ops) |
| `kit.Game` / `GameMeta` | `Game` / `Meta` (`..Meta::DEFAULT`) |
| `r.Send(p, f)` / `r.Identical(f)` | `r.send(&p, &f)` / `r.identical(&f)` |
| `in.Kind`/`in.Rune`/`in.Key` | `match input { Input::Char(c) => …, Input::Key(k) => … }` |
| `kit.Resolve(in, ctx)` | `input.resolve(ctx)` |
| `r.End(res)` with `kit.Result` | `r.end(&res)` with `Outcome` (renamed: `Result` would shadow Rust's) |
| `r.Rand()` | `r.rng_u64()` / `r.rng_below(n)` |
| `acct.Store().Get/Set/Delete` | `r.kv_get/kv_set/kv_delete(&p, …)` (`Option` instead of `(v, ok)`) |
| `f.SetGrapheme(...)` | `f.set_grapheme(...)` (≤3 code points per cell, refused otherwise) |

## Toolchain notes

- **Target**: `wasm32-wasip1`. The crate type that works is `cdylib` — it
  produces a WASI *reactor* (exports `memory` + `_initialize`; no `_start`),
  which is exactly what the arcade host instantiates. The scaffold sets
  `crate-type = ["cdylib", "rlib"]` (`rlib` so `cargo test` links natively).
- **Release profile lives in YOUR Cargo.toml.** Cargo applies `[profile.release]`
  only at the leaf crate — the scaffold ships `opt-level = "s"`, `lto = true`,
  `strip = true`, `panic = "abort"`, and artifacts land around ~130 KiB. Don't
  delete that block; without it your artifact is several MB of debug build.
- **Versioning**: your `Cargo.toml` pins this crate as a git dependency on the
  exact kit release tag your CLI shipped with
  (`shellcade-kit = { git = "https://github.com/shellcade/kit", tag = "vX.Y.Z" }`).
  Upgrade by bumping the tag to a newer kit release.
- **Panics**: a guest panic is a contained fault — the host settles the room
  and destroys the instance. The default panic hook's message reaches the room
  log via virtualized stderr; you don't need (and shouldn't install) a custom
  hook.
- **Determinism**: never read wall time, environment, or ambient entropy —
  hibernation reconstructs your room from linear memory and replays the room
  clock/seed (`shellcade-kit check` verifies byte-identical frames across a
  snapshot/restore).

## What the SDK owns (so you can't hold it wrong)

Frame delivery is ABI v2 frame-delta: per-player baselines, host-authoritative
epochs, keyframes on first send / roster change, and an in-call keyframe retry
whenever the host rejects a delta (e.g. after a hibernation restore) — all
inside `r.send`/`r.identical`. You compose a `Frame`; the SDK does the rest.

The wire decoders are tolerant: unknown input kinds/keys and trailing payload
bytes are dropped before your `on_input` ever runs, so future ABI growth never
faults your shipped artifact.
