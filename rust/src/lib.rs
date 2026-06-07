//! # shellcade-kit — the Rust guest SDK for shellcade ABI v2
//!
//! Author a wasm arcade game as a [`Handler`] impl plus one macro invocation —
//! no plumbing, no `unsafe`, no Extism types:
//!
//! ```no_run
//! use shellcade_kit::prelude::*;
//!
//! struct MyGame;
//!
//! impl Game for MyGame {
//!     fn meta(&self) -> Meta {
//!         Meta {
//!             slug: "my-game",
//!             name: "My Game",
//!             short_description: "One line for the lobby.",
//!             min_players: 1,
//!             max_players: 1,
//!             ..Meta::DEFAULT
//!         }
//!     }
//!     fn new_room(&self, _cfg: &RoomConfig) -> Box<dyn Handler> {
//!         Box::new(MyRoom { frame: Frame::new() })
//!     }
//! }
//!
//! struct MyRoom {
//!     frame: Frame,
//! }
//!
//! impl Handler for MyRoom {
//!     fn on_start(&mut self, r: &mut Room) {
//!         r.set_input_context(InputContext::Nav);
//!     }
//!     fn on_input(&mut self, r: &mut Room, _p: Player, input: Input) {
//!         if input.resolve(InputContext::Nav) == Action::Confirm {
//!             self.frame.clear();
//!             self.frame.text(1, 2, "hello, arcade", Style::new(WHITE, ATTR_BOLD));
//!             r.identical(&self.frame);
//!         }
//!     }
//! }
//!
//! shellcade_kit::shellcade_game!(MyGame);
//! # fn main() {}
//! ```
//!
//! Build for the arcade with `cargo build --release --target wasm32-wasip1`
//! and validate with `shellcade-kit check` (see the crate README for the
//! quickstart; this crate is consumed as a git dependency pinned to a kit
//! release tag).
//!
//! The SDK owns the entire ABI v2 frame-delta discipline (ABI.md §4.5–§4.7):
//! per-player baselines, host-authoritative epochs, keyframe bootstrap and
//! roster-change invalidation, and the in-call keyframe retry on epoch
//! rejection — [`Room::send`] / [`Room::identical`] are all a game touches.
//! Wire decoding is tolerant (unknown input is dropped, never delivered as
//! degraded values), and the only `unsafe` in the crate is the host FFI core
//! in `host.rs`.

#![deny(unsafe_code)]

mod broadcast;
mod frame;
mod host;
mod input;
mod json;
mod rng;
mod room;
mod types;
mod wire;

// Byte-verified RUN-LIST delta encoder — doc(hidden) public ONLY so the
// kit/crossverify golden-vector harness binds this exact encoder
// byte-identical to the Go reference. Not author-facing.
#[doc(hidden)]
pub mod delta;

// The dispatch runtime behind shellcade_game! — doc(hidden), not author-facing.
#[doc(hidden)]
#[path = "rt.rs"]
pub mod __rt;

pub use frame::{
    Cell, Color, Frame, Style, ATTR_BOLD, ATTR_DIM, ATTR_REVERSE, ATTR_UNDERLINE, COLS, CYAN,
    DIM_GRAY, GREEN, RED, ROWS, WHITE, YELLOW,
};
pub use input::{Action, Input, InputContext, Key};
pub use room::Room;
pub use types::{
    Aggregation, ConfigKeySpec, ConfigType, Direction, Kind, Leaderboard, MergeRule, Meta,
    MetricFormat, Mode, Outcome, Player, PlayerResult, RoomConfig, Status, CTX_FEAT_ROSTER_EPOCH, Lifecycle,
};

// Native-only scriptable host double for `cargo test` of games and the SDK
// itself. Doc-hidden: a test convenience, not stable API.
#[cfg(not(target_arch = "wasm32"))]
#[doc(hidden)]
pub use host::{reset_test_host, with_test_host, SentPayload, TestHost};

/// The module entry: static metadata plus the per-room behavior factory
/// (mirrors Go `kit.Game`).
pub trait Game {
    fn meta(&self) -> Meta;
    fn new_room(&self, cfg: &RoomConfig) -> Box<dyn Handler>;
}

/// A game's per-room behavior — the six-callback surface (mirrors Go
/// `kit.Handler`). `on_wake` is the host heartbeat (~20×/sec); there are no
/// ticks, timers, or frame callbacks. Optional callbacks default to no-ops;
/// `on_start` and `on_input` are required — a game must declare its entry and
/// input behavior.
pub trait Handler {
    fn on_start(&mut self, r: &mut Room);
    fn on_join(&mut self, _r: &mut Room, _p: Player) {}
    fn on_leave(&mut self, _r: &mut Room, _p: Player) {}
    fn on_input(&mut self, r: &mut Room, p: Player, input: Input);
    fn on_wake(&mut self, _r: &mut Room) {}
    fn on_close(&mut self, _r: &mut Room) {}
}

/// Everything a game file needs: `use shellcade_kit::prelude::*;`
pub mod prelude {
    pub use crate::{
        Action, Aggregation, Cell, Color, Direction, Frame, Game, Handler, Input, InputContext,
        Key, Kind, Leaderboard, MergeRule, Meta, MetricFormat, Mode, Outcome, Player,
        PlayerResult, Room, RoomConfig, Status, Style, ATTR_BOLD, ATTR_DIM, ATTR_REVERSE,
        ATTR_UNDERLINE, COLS, CYAN, DIM_GRAY, GREEN, RED, ROWS, WHITE, YELLOW,
    };
}

/// Register a [`Game`] as this wasm module's shellcade guest: generates the
/// eight ABI exports and the per-instance room cell. The argument is an
/// expression constructing your game (a unit struct name works as-is).
///
/// The expansion is fully safe code — a game crate can (and the scaffold does)
/// carry `#![forbid(unsafe_code)]`. Exports are gated to wasm32 so native test
/// builds never interpose libc symbols (`close`!); under `cargo test` your
/// game logic runs against the SDK's in-memory test host instead.
#[macro_export]
macro_rules! shellcade_game {
    ($game:expr) => {
        // Native builds (cargo test) have no exports, so the registration is
        // surfaced as a doc-hidden pub fn instead: it type-checks the Game
        // impl, roots the whole game for dead-code analysis, and hands tests
        // a constructor.
        #[cfg(not(target_arch = "wasm32"))]
        #[doc(hidden)]
        pub fn __shellcade_game() -> impl $crate::Game {
            $game
        }

        #[cfg(target_arch = "wasm32")]
        const _: () = {
            ::std::thread_local! {
                // The per-instance room cell: one plugin instance == one room
                // (ABI §1) and callbacks are serial, so a thread-local RefCell
                // holds the handler; a serial-callback violation is a contained
                // borrow panic, never UB.
                static __SHELLCADE_HANDLER: ::core::cell::RefCell<
                    ::core::option::Option<::std::boxed::Box<dyn $crate::Handler>>,
                > = ::core::cell::RefCell::new(::core::option::Option::None);
            }

            #[unsafe(no_mangle)]
            extern "C" fn shellcade_abi() -> i32 {
                $crate::__rt::abi()
            }
            #[unsafe(no_mangle)]
            extern "C" fn meta() -> i32 {
                $crate::__rt::meta(&$game)
            }
            #[unsafe(no_mangle)]
            extern "C" fn start() -> i32 {
                $crate::__rt::start(&__SHELLCADE_HANDLER, &$game)
            }
            #[unsafe(no_mangle)]
            extern "C" fn join() -> i32 {
                $crate::__rt::join(&__SHELLCADE_HANDLER)
            }
            #[unsafe(no_mangle)]
            extern "C" fn leave() -> i32 {
                $crate::__rt::leave(&__SHELLCADE_HANDLER)
            }
            #[unsafe(no_mangle)]
            extern "C" fn input() -> i32 {
                $crate::__rt::input(&__SHELLCADE_HANDLER)
            }
            #[unsafe(no_mangle)]
            extern "C" fn wake() -> i32 {
                $crate::__rt::wake(&__SHELLCADE_HANDLER)
            }
            #[unsafe(no_mangle)]
            extern "C" fn close() -> i32 {
                $crate::__rt::close(&__SHELLCADE_HANDLER)
            }
        };
    };
}
