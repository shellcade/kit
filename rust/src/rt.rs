//! The dispatch runtime behind `shellcade_game!` — `#[doc(hidden)]`, not
//! author-facing. Each function backs one ABI export: decode the CallContext,
//! run the roster-change backstop, gate on decode health (a set bad flag, an
//! out-of-roster player index, or an unknown input is DROPPED — a Handler is
//! never invoked with degraded zeros), then dispatch.
//!
//! Lives outside the macro so it compiles on every target and is unit-tested
//! natively against the test host; the macro only generates the eight
//! wasm-gated `#[unsafe(no_mangle)]` trampolines and the room cell.

use std::cell::RefCell;
use std::thread::LocalKey;

use crate::host::{read_input, write_output};
use crate::input::{Input, Key};
use crate::room::{Room, STATE};
use crate::rng::SplitMix64;
use crate::types::Player;
use crate::wire::{decode_ctx, encode_meta, Rd};
use crate::{Game, Handler};

/// ABI major version this SDK targets.
pub const ABI_VERSION: u32 = 2;

/// The per-instance handler cell type the macro generates (`thread_local!` +
/// `RefCell` — fully safe; an ABI §1 serial-callback violation is a contained
/// borrow panic, never UB).
pub type HandlerCell = LocalKey<RefCell<Option<Box<dyn Handler>>>>;

/// Decode a callback: CallContext + roster backstop + PRNG seeding.
fn decode_call(input: &[u8]) -> (Room, Rd<'_>) {
    let (ctx, r) = decode_ctx(input);
    STATE.with(|s| {
        let mut st = s.borrow_mut();
        if st.rng.is_none() {
            st.rng = Some(SplitMix64::new(ctx.cfg.seed));
        }
        st.roster_gate(&ctx.members);
    });
    (Room { ctx }, r)
}

/// Gate + resolve the trailing playerIdx (join/leave/input): a short read or
/// an out-of-roster index drops the callback.
fn decode_player(room: &Room, r: &mut Rd<'_>) -> Option<Player> {
    let idx = r.u32() as usize;
    if r.bad() || idx >= room.ctx.members.len() {
        return None;
    }
    Some(room.ctx.members[idx].clone())
}

/// Gate + decode the input event into the [`Input`] sum type. Unknown kind,
/// unknown/unassigned key, invalid code point, or a short read ⇒ `None`
/// (tolerant reader: drop, no fault, no callback). Trailing bytes beyond the
/// fields read here are ignored by construction.
fn decode_input(r: &mut Rd<'_>) -> Option<Input> {
    let kind = r.u8();
    let rune = r.u32();
    let key = r.u8();
    if r.bad() {
        return None;
    }
    match kind {
        0 => char::from_u32(rune).map(Input::Char),
        1 => Key::from_wire(key).map(Input::Key),
        _ => None, // unknown kind: ignore (additive ABI growth)
    }
}

pub fn abi() -> i32 {
    write_output(&ABI_VERSION.to_le_bytes());
    0
}

pub fn meta(game: &dyn Game) -> i32 {
    write_output(&encode_meta(&game.meta()));
    0
}

pub fn start(cell: &'static HandlerCell, game: &dyn Game) -> i32 {
    let input = read_input();
    let (mut room, _) = decode_call(&input);
    let handler = game.new_room(&room.ctx.cfg);
    cell.with(|c| *c.borrow_mut() = Some(handler));
    with_handler(cell, |h| h.on_start(&mut room));
    0
}

pub fn join(cell: &'static HandlerCell) -> i32 {
    let input = read_input();
    let (mut room, mut r) = decode_call(&input);
    let Some(p) = decode_player(&room, &mut r) else {
        return 0;
    };
    with_handler(cell, |h| h.on_join(&mut room, p));
    0
}

pub fn leave(cell: &'static HandlerCell) -> i32 {
    let input = read_input();
    let (mut room, mut r) = decode_call(&input);
    let Some(p) = decode_player(&room, &mut r) else {
        return 0;
    };
    with_handler(cell, |h| h.on_leave(&mut room, p));
    0
}

pub fn input(cell: &'static HandlerCell) -> i32 {
    let bytes = read_input();
    let (mut room, mut r) = decode_call(&bytes);
    let Some(p) = decode_player(&room, &mut r) else {
        return 0;
    };
    let Some(ev) = decode_input(&mut r) else {
        return 0; // unknown kind/key or short read: ignore, no callback
    };
    with_handler(cell, |h| h.on_input(&mut room, p, ev));
    0
}

pub fn wake(cell: &'static HandlerCell) -> i32 {
    let input = read_input();
    let (mut room, _) = decode_call(&input);
    with_handler(cell, |h| h.on_wake(&mut room));
    0
}

pub fn close(cell: &'static HandlerCell) -> i32 {
    let input = read_input();
    let (mut room, _) = decode_call(&input);
    with_handler(cell, |h| h.on_close(&mut room));
    0
}

/// Run `f` against the live handler, if `start` has installed one (the host
/// always starts first; a missing handler degrades to a drop, mirroring Go's
/// `handler != nil` guards). The handler box is moved out of the cell for the
/// duration of the callback so `Room` methods can never re-enter the cell.
fn with_handler(cell: &'static HandlerCell, f: impl FnOnce(&mut Box<dyn Handler>)) {
    let taken = cell.with(|c| c.borrow_mut().take());
    if let Some(mut h) = taken {
        f(&mut h);
        cell.with(|c| {
            let mut slot = c.borrow_mut();
            // `start` may have replaced the handler mid-callback (a restart
            // pattern); only restore ours if the cell is still empty.
            if slot.is_none() {
                *slot = Some(h);
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::host::{reset_test_host, with_test_host};
    use crate::types::{Meta, RoomConfig};
    use crate::wire::Buf;
    use crate::{Frame, Outcome};

    // A counting game/handler pair for dispatch tests.
    struct TestGame;
    #[derive(Default)]
    struct TestRoom {
        starts: usize,
        joins: Vec<String>,
        inputs: Vec<Input>,
        wakes: usize,
    }

    thread_local! {
        static SEEN: RefCell<TestRoom> = RefCell::new(TestRoom::default());
        static CELL: RefCell<Option<Box<dyn Handler>>> = RefCell::new(None);
    }

    impl Game for TestGame {
        fn meta(&self) -> Meta {
            Meta { slug: "test", name: "Test", ..Meta::DEFAULT }
        }
        fn new_room(&self, _cfg: &RoomConfig) -> Box<dyn Handler> {
            Box::new(TestHandler)
        }
    }

    struct TestHandler;
    impl Handler for TestHandler {
        fn on_start(&mut self, _r: &mut Room) {
            SEEN.with(|s| s.borrow_mut().starts += 1);
        }
        fn on_input(&mut self, r: &mut Room, p: Player, input: Input) {
            SEEN.with(|s| s.borrow_mut().inputs.push(input));
            // exercise an effect inside a callback: must not panic (no cell
            // re-entry, no STATE borrow conflict)
            r.identical(&Frame::new());
            let _ = r.kv_get(&p, "k");
            let _ = &Outcome::default();
        }
        fn on_join(&mut self, _r: &mut Room, p: Player) {
            SEEN.with(|s| s.borrow_mut().joins.push(p.handle));
        }
        fn on_wake(&mut self, _r: &mut Room) {
            SEEN.with(|s| s.borrow_mut().wakes += 1);
        }
    }

    fn ctx_with_members(handles: &[&str]) -> Buf {
        let mut w = Buf::new();
        w.i64(5); // now
        w.i64(42); // seed
        w.u8(1);
        w.u8(0);
        w.u16(4);
        w.u16(1);
        w.u16(handles.len() as u16);
        for h in handles {
            w.str(h);
            w.str(&format!("id-{h}"));
            w.str("conn");
            w.u8(1);
        }
        w.u8(0); // settled
        w
    }

    fn fresh() {
        reset_test_host();
        STATE.with(|s| *s.borrow_mut() = crate::broadcast::SdkState::new());
        SEEN.with(|s| *s.borrow_mut() = TestRoom::default());
        CELL.with(|c| *c.borrow_mut() = None);
    }

    fn run_export(payload: Vec<u8>, f: impl FnOnce() -> i32) -> i32 {
        with_test_host(|h| h.input = payload);
        f()
    }

    #[test]
    fn abi_writes_version_2_le() {
        fresh();
        assert_eq!(abi(), 0);
        with_test_host(|h| assert_eq!(h.output, 2u32.to_le_bytes()));
    }

    #[test]
    fn meta_encodes_via_sdk_serializer() {
        fresh();
        assert_eq!(meta(&TestGame), 0);
        with_test_host(|h| assert!(h.output.starts_with(&[4, 0, b't', b'e', b's', b't'])));
    }

    #[test]
    fn full_callback_flow_start_join_input_wake() {
        fresh();
        assert_eq!(run_export(ctx_with_members(&[]).b, || start(&CELL, &TestGame)), 0);

        let mut w = ctx_with_members(&["alice"]);
        w.u32(0); // playerIdx
        assert_eq!(run_export(w.b, || join(&CELL)), 0);

        let mut w = ctx_with_members(&["alice"]);
        w.u32(0);
        w.u8(0); // kind: rune
        w.u32('5' as u32);
        w.u8(0); // key
        assert_eq!(run_export(w.b, || input(&CELL)), 0);

        assert_eq!(run_export(ctx_with_members(&["alice"]).b, || wake(&CELL)), 0);

        SEEN.with(|s| {
            let s = s.borrow();
            assert_eq!(s.starts, 1);
            assert_eq!(s.joins, vec!["alice"]);
            assert_eq!(s.inputs, vec![Input::Char('5')]);
            assert_eq!(s.wakes, 1);
        });
        // the input handler broadcast a frame via the test host
        with_test_host(|h| assert_eq!(h.sent.len(), 1));
    }

    #[test]
    fn gates_drop_bad_decodes_not_deliver_zeros() {
        fresh();
        run_export(ctx_with_members(&["a"]).b, || start(&CELL, &TestGame));

        // out-of-roster index
        let mut w = ctx_with_members(&["a"]);
        w.u32(9);
        run_export(w.b, || join(&CELL));

        // short read: no trailing playerIdx at all
        run_export(ctx_with_members(&["a"]).b, || join(&CELL));

        // unknown input kind
        let mut w = ctx_with_members(&["a"]);
        w.u32(0);
        w.u8(7); // future kind
        w.u32(0);
        w.u8(0);
        run_export(w.b, || input(&CELL));

        // unknown named key (KeyNone / future)
        let mut w = ctx_with_members(&["a"]);
        w.u32(0);
        w.u8(1);
        w.u32(0);
        w.u8(200);
        run_export(w.b, || input(&CELL));

        // truncated input event
        let mut w = ctx_with_members(&["a"]);
        w.u32(0);
        w.u8(0); // kind only, no rune/key bytes
        run_export(w.b, || input(&CELL));

        SEEN.with(|s| {
            let s = s.borrow();
            assert!(s.joins.is_empty(), "bad joins dropped");
            assert!(s.inputs.is_empty(), "bad inputs dropped");
        });
    }

    #[test]
    fn trailing_bytes_beyond_known_fields_are_ignored() {
        fresh();
        run_export(ctx_with_members(&["a"]).b, || start(&CELL, &TestGame));
        let mut w = ctx_with_members(&["a"]);
        w.u32(0);
        w.u8(0);
        w.u32('x' as u32);
        w.u8(0);
        w.b.extend_from_slice(&[0xde, 0xad, 0xbe, 0xef]); // future growth
        run_export(w.b, || input(&CELL));
        SEEN.with(|s| assert_eq!(s.borrow().inputs, vec![Input::Char('x')]));
    }

    #[test]
    fn callbacks_before_start_are_dropped() {
        fresh();
        let mut w = ctx_with_members(&["a"]);
        w.u32(0);
        run_export(w.b, || join(&CELL)); // no handler yet: drop, return 0
        SEEN.with(|s| assert!(s.borrow().joins.is_empty()));
    }
}
