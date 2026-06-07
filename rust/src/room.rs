//! The `Room` handle: local reads answered from the cached CallContext (zero
//! host calls), effects via host functions with the SDK-owned frame-diffing
//! discipline. A `Room` is valid only inside the callback that received it.

use std::cell::RefCell;

use crate::broadcast::{SdkState, BROADCAST_SLOT};
use crate::frame::Frame;
use crate::host;
use crate::input::InputContext;
use crate::rng::SplitMix64;
use crate::types::{MergeRule, Outcome, Player, RoomConfig};
use crate::wire::{encode_outcome, CallCtx};

thread_local! {
    /// The per-instance SDK state surviving across callbacks: baseline table,
    /// PRNG, roster fingerprint. Fully safe (see `SdkState` docs).
    pub(crate) static STATE: RefCell<SdkState> = RefCell::new(SdkState::new());
}

/// The authoring surface handed to every [`crate::Handler`] callback.
pub struct Room {
    pub(crate) ctx: CallCtx,
}

impl Room {
    // ---- local reads (zero host calls) ----------------------------------------

    /// The current roster. On `on_leave`, the departed player is the FINAL
    /// entry (no longer a member, present for that callback only).
    pub fn members(&self) -> &[Player] {
        &self.ctx.members
    }

    /// Whether `p` is in the current roster.
    pub fn has(&self, p: &Player) -> bool {
        self.ctx.members.contains(p)
    }

    /// Roster size (see [`Room::members`] for the `on_leave` caveat).
    pub fn count(&self) -> usize {
        self.ctx.members.len()
    }

    pub fn config(&self) -> &RoomConfig {
        &self.ctx.cfg
    }

    /// The room clock in unix nanoseconds — monotonic non-decreasing, the only
    /// time a game may use (never wall time; hibernation depends on it).
    pub fn now_unix_nanos(&self) -> i64 {
        self.ctx.now_unix_nanos
    }

    /// Whether the room has already settled (`end` was called).
    pub fn settled(&self) -> bool {
        self.ctx.settled
    }

    /// Next value from the room PRNG (seeded from the room seed; state lives in
    /// linear memory so hibernation replays the exact sequence).
    pub fn rng_u64(&mut self) -> u64 {
        STATE.with(|s| {
            let mut st = s.borrow_mut();
            let seed = self.ctx.cfg.seed;
            st.rng.get_or_insert_with(|| SplitMix64::new(seed)).next_u64()
        })
    }

    /// Uniform value in `0..n` from the room PRNG (`0` when `n == 0`).
    pub fn rng_below(&mut self, n: u64) -> u64 {
        STATE.with(|s| {
            let mut st = s.borrow_mut();
            let seed = self.ctx.cfg.seed;
            st.rng.get_or_insert_with(|| SplitMix64::new(seed)).below(n)
        })
    }

    // ---- effects (host calls) ---------------------------------------------------

    /// Deliver a frame to one player. The SDK diffs against that player slot's
    /// baseline, keyframes on bootstrap/roster change, mirrors the
    /// host-returned epoch, and retries a rejected delta as a keyframe in-call
    /// — no render is ever lost. A departed/unknown player is a no-op.
    pub fn send(&mut self, p: &Player, f: &Frame) {
        let Some(idx) = self.index(p) else {
            return;
        };
        STATE.with(|s| s.borrow_mut().send_slot(idx, f));
    }

    /// Broadcast one frame to every player (host `identical`). On accept the
    /// SDK reconciles EVERY per-player baseline to this frame, so a later
    /// [`Room::send`] diffs correctly.
    pub fn identical(&mut self, f: &Frame) {
        STATE.with(|s| s.borrow_mut().send_slot(BROADCAST_SLOT, f));
    }

    /// Select how subsequent input resolves (Nav/Command/Text). Host-side
    /// state: survives hibernation; do not re-issue on resume.
    pub fn set_input_context(&mut self, ctx: InputContext) {
        host::host_set_input_context(ctx as i64);
    }

    /// Settle the room exactly once with the final rankings.
    pub fn end(&mut self, res: &Outcome) {
        host::host_end(&encode_outcome(res, &self.ctx.members));
    }

    /// Record a leaderboard result without ending the room.
    pub fn post(&mut self, res: &Outcome) {
        host::host_post(&encode_outcome(res, &self.ctx.members));
    }

    /// Host-side info log (also where panics surface — the default panic hook
    /// prints to virtualized stderr, which lands in the room log).
    pub fn log(&mut self, msg: &str) {
        host::host_log(1, msg);
    }

    // ---- durable per-user KV + read-only config ----------------------------------

    /// Durable per-user KV read; `None` = not found. Namespacing is host-side:
    /// this game + this account, derived from the roster index — a guest can
    /// never name another namespace.
    pub fn kv_get(&self, p: &Player, key: &str) -> Option<Vec<u8>> {
        let idx = self.kv_index(p)?;
        host::host_kv_get(idx, key)
    }

    /// Durable per-user KV write with the account-merge `rule`.
    pub fn kv_set(&mut self, p: &Player, key: &str, value: &[u8], rule: MergeRule) {
        let Some(idx) = self.kv_index(p) else {
            return;
        };
        host::host_kv_set(idx, key, value, rule.as_str());
    }

    /// Durable per-user KV delete.
    pub fn kv_delete(&mut self, p: &Player, key: &str) {
        let Some(idx) = self.kv_index(p) else {
            return;
        };
        host::host_kv_delete(idx, key);
    }

    /// Read-only per-game config; `None` = not found.
    pub fn config_get(&self, key: &str) -> Option<Vec<u8>> {
        host::host_config_get(key)
    }

    // ---- internals ---------------------------------------------------------------

    fn index(&self, p: &Player) -> Option<usize> {
        self.ctx.members.iter().position(|m| m == p)
    }

    /// KV resolves a departed player (delivered as the final roster entry on
    /// `on_leave`) by account id when exact equality misses — mirroring the Go
    /// SDK, so "save on leave" works.
    fn kv_index(&self, p: &Player) -> Option<usize> {
        self.index(p)
            .or_else(|| self.ctx.members.iter().position(|m| m.account_id == p.account_id))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::host::{reset_test_host, with_test_host};
    use crate::types::Kind;
    use crate::wire::CallCtx;

    fn room_with(members: Vec<Player>) -> Room {
        Room {
            ctx: CallCtx {
                now_unix_nanos: 1,
                cfg: RoomConfig { seed: 99, ..Default::default() },
                members: std::rc::Rc::new(members),
                settled: false,
                roster_epoch: None,
                roster_unchanged: false,
            },
        }
    }

    fn player(id: &str) -> Player {
        Player { account_id: id.into(), handle: id.into(), conn: "c".into(), kind: Kind::Member }
    }

    #[test]
    fn send_to_unknown_player_is_a_noop() {
        reset_test_host();
        STATE.with(|s| *s.borrow_mut() = SdkState::new());
        let mut r = room_with(vec![player("a")]);
        r.send(&player("ghost"), &Frame::new());
        with_test_host(|h| assert!(h.sent.is_empty()));
        r.send(&player("a"), &Frame::new());
        with_test_host(|h| assert_eq!(h.sent.len(), 1));
    }

    #[test]
    fn rng_is_seeded_from_room_seed_and_persists() {
        reset_test_host();
        STATE.with(|s| *s.borrow_mut() = SdkState::new());
        let mut r = room_with(vec![]);
        let a = r.rng_u64();
        let b = r.rng_u64();
        assert_ne!(a, b, "sequence advances");
        let mut reference = crate::rng::SplitMix64::new(99);
        assert_eq!(a, reference.next_u64(), "seeded from ctx seed");
    }

    #[test]
    fn kv_resolves_departed_player_by_account_id() {
        reset_test_host();
        let mut r = room_with(vec![player("a"), player("b")]);
        // A clone whose handle changed (e.g. departed snapshot) still resolves.
        let mut departed = player("b");
        departed.handle = "old-handle".into();
        r.kv_set(&departed, "wins", b"3", MergeRule::Sum);
        with_test_host(|h| {
            assert_eq!(h.kv.get(&(1, "wins".to_string())).map(|v| v.as_slice()), Some(&b"3"[..]));
        });
        assert_eq!(r.kv_get(&player("b"), "wins"), Some(b"3".to_vec()));
        assert_eq!(r.kv_get(&player("nope"), "wins"), None);
    }

    #[test]
    fn outcome_and_log_reach_the_host() {
        reset_test_host();
        let mut r = room_with(vec![player("a")]);
        r.log("hello");
        r.end(&Outcome::default());
        r.set_input_context(InputContext::Text);
        with_test_host(|h| {
            assert_eq!(h.logs, vec![(1, "hello".to_string())]);
            assert_eq!(h.ends.len(), 1);
            assert_eq!(h.input_contexts, vec![2]);
        });
    }
}
