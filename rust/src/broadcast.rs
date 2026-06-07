//! Per-consumer frame-diffing state (ABI v2 §4.5–§4.7), mirroring the Go SDK's
//! baseline table: one slot per roster index plus the broadcast slot. The SDK
//! owns ALL of it — baselines, host-authoritative epochs, keyframe bootstrap,
//! roster-change invalidation, and the normative in-call keyframe retry on
//! epoch rejection — so a game cannot hold the rules wrong.
//!
//! All buffers are allocated once (lazily, on the first send) and reused
//! forever: the steady state is allocation-free apart from the host
//! transport's kernel staging copy.

use crate::delta::{encode, KEYFRAME_BYTES};
use crate::frame::{Frame, FRAME_BYTES};
use crate::host;
use crate::rng::SplitMix64;
use crate::types::Player;

/// Fixed roster ceiling for per-index baselines. At 24-byte cells the table is
/// Per-slot baselines are lazily allocated (~45 KiB per actively-sent-to
/// consumer), so linear memory tracks the ACTIVE roster, not the cap — far
/// under the 32 MiB cap.
pub(crate) const ROSTER_CAP: usize = 1024;
/// The broadcast (`identical`) slot index within the baseline table.
pub(crate) const BROADCAST_SLOT: usize = ROSTER_CAP;
const SLOTS: usize = ROSTER_CAP + 1;

/// The per-instance SDK state that must survive across callbacks: the baseline
/// table, the room PRNG, and the roster fingerprint. Lives in a thread-local
/// `RefCell` (fully safe; wasm32-wasip1 is single-threaded and callbacks are
/// serial per ABI §1 — a violation is a contained borrow panic, never UB).
pub(crate) struct SdkState {
    /// Per-slot baselines (FRAME_BYTES each), lazily allocated on a slot's
    /// first commit so memory tracks the active roster, not ROSTER_CAP.
    baselines: Vec<Vec<u8>>,
    epoch: Vec<u32>,
    present: Vec<bool>,
    /// Reused pack scratch for the current frame (FRAME_BYTES).
    packed: Vec<u8>,
    /// Reused delta-container scratch (KEYFRAME_BYTES worst case).
    scratch: Vec<u8>,
    pub rng: Option<SplitMix64>,
    last_roster: Option<u64>,
    /// Roster-epoch mode cache: the members decoded at the last full form,
    /// shared into each callback's CallCtx with zero member allocations.
    pub roster_cache: Option<(u32, std::rc::Rc<Vec<crate::types::Player>>)>,
}

impl SdkState {
    pub fn new() -> Self {
        SdkState {
            baselines: vec![Vec::new(); SLOTS],
            epoch: vec![0; SLOTS],
            present: vec![false; SLOTS],
            packed: Vec::new(),
            scratch: Vec::new(),
            rng: None,
            last_roster: None,
            roster_cache: None,
        }
    }

    fn ensure_buffers(&mut self) {
        if self.packed.is_empty() {
            self.packed = vec![0u8; FRAME_BYTES];
            self.scratch = vec![0u8; KEYFRAME_BYTES];
        }
    }

    #[cfg(test)]
    fn baseline(&self, slot: usize) -> &[u8] {
        &self.baselines[slot]
    }

    /// Clear every present flag, forcing the next send to each slot to a
    /// keyframe — called on any roster change (indices renumber; the host
    /// clears its caches and the guest mirrors).
    pub fn invalidate_baselines(&mut self) {
        self.present.fill(false);
    }

    /// The roster-change backstop run at every callback decode: a cheap
    /// FNV-1a fingerprint of the roster (count + account ids + kinds) compared
    /// to the previous callback's; any change invalidates every baseline.
    pub fn roster_gate(&mut self, members: &[Player]) {
        let print = roster_fingerprint(members);
        if self.last_roster != Some(print) {
            self.invalidate_baselines();
            self.last_roster = Some(print);
        }
    }

    /// Diff `frame` against `slot`'s baseline and deliver it via the host
    /// (`send` for a player slot, `identical` for [`BROADCAST_SLOT`]),
    /// honoring the v2 rules: keyframe when the slot is not present (first
    /// send / roster change / prior rejection) or when the run-list meets the
    /// inclusive keyframe budget; mirror the host-returned epoch; and on a
    /// rejected delta (returned ≠ sent) immediately RE-SEND this same frame as
    /// a keyframe stamped with the returned epoch — still on-stack, keyframes
    /// are unconditionally accepted, so no render is ever lost (§4.6
    /// obligation 2). On accept of a broadcast, reconcile EVERY per-index
    /// baseline so a later per-player send diffs against the correct frame.
    pub fn send_slot(&mut self, slot: usize, frame: &Frame) {
        if slot >= SLOTS {
            return;
        }
        self.ensure_buffers();
        frame.pack_into(&mut self.packed);

        let sent_epoch = self.epoch[slot];
        let was_delta = self.present[slot];
        // present implies the slot buffer was allocated by a prior commit;
        // the keyframe path never reads the baseline, so an empty slice is
        // safe when !was_delta.
        let n = encode(
            &self.baselines[slot],
            &self.packed,
            &mut self.scratch,
            sent_epoch,
            !was_delta,
        );
        let mut returned = deliver(slot, &self.scratch[..n]);

        if returned != sent_epoch && was_delta {
            // Rejected delta (hibernation restore, baseline loss): resync to
            // the host's epoch and retry this same frame as a keyframe.
            let n = encode(
                &self.baselines[slot],
                &self.packed,
                &mut self.scratch,
                returned,
                true,
            );
            returned = deliver(slot, &self.scratch[..n]);
        }

        // Adopt the baseline + epoch: the host now holds this exact frame.
        self.commit(slot, returned);
        if slot == BROADCAST_SLOT {
            // Reconcile only ALLOCATED per-index slots — materializing all
            // ROSTER_CAP would copy ~45 MiB per broadcast at cap 1024. A
            // skipped slot stays not-present and recovers via its first
            // per-player send opening with a keyframe (mirrors the host).
            for i in 0..ROSTER_CAP {
                if self.baselines[i].is_empty() {
                    self.present[i] = false;
                } else {
                    self.commit(i, returned);
                }
            }
        }
    }

    fn commit(&mut self, slot: usize, returned_epoch: u32) {
        if self.baselines[slot].is_empty() {
            self.baselines[slot] = vec![0u8; FRAME_BYTES];
        }
        self.baselines[slot].copy_from_slice(&self.packed);
        self.epoch[slot] = returned_epoch;
        self.present[slot] = true;
    }

    #[cfg(test)]
    pub(crate) fn slot_state(&self, slot: usize) -> (u32, bool) {
        (self.epoch[slot], self.present[slot])
    }

    #[cfg(test)]
    pub(crate) fn slot_baseline_equals_packed(&self, slot: usize) -> bool {
        self.baseline(slot) == &self.packed[..]
    }
}

fn deliver(slot: usize, payload: &[u8]) -> u32 {
    if slot == BROADCAST_SLOT {
        host::host_identical(payload)
    } else {
        host::host_send(slot, payload)
    }
}

fn roster_fingerprint(members: &[Player]) -> u64 {
    let mut h: u64 = 1469598103934665603; // FNV-1a offset
    let mut mix = |b: u8| {
        h ^= u64::from(b);
        h = h.wrapping_mul(1099511628211);
    };
    mix(members.len() as u8);
    mix((members.len() >> 8) as u8);
    for p in members {
        for b in p.account_id.as_bytes() {
            mix(*b);
        }
        mix(0);
        mix(p.kind as u8);
        mix(b'|');
    }
    h
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::delta::FLAG_KEYFRAME;
    use crate::frame::{Style, WHITE};
    use crate::host::{reset_test_host, with_test_host};
    use crate::types::Kind;

    fn frame_with(s: &str) -> Frame {
        let mut f = Frame::new();
        f.text(0, 0, s, Style::new(WHITE, 0));
        f
    }

    fn player(id: &str) -> Player {
        Player { account_id: id.into(), handle: id.into(), conn: "c".into(), kind: Kind::Member }
    }

    #[test]
    fn first_send_is_keyframe_then_delta_and_epoch_mirrors() {
        reset_test_host();
        let mut st = SdkState::new();

        st.send_slot(0, &frame_with("hi"));
        let (payloads, epochs): (Vec<u8>, u32) = with_test_host(|h| {
            assert_eq!(h.sent.len(), 1);
            assert_eq!(h.sent[0].player_idx, Some(0));
            (vec![h.sent[0].payload[0]], u32::from_le_bytes(h.sent[0].payload[1..5].try_into().unwrap()))
        });
        assert_eq!(payloads[0] & FLAG_KEYFRAME, FLAG_KEYFRAME, "first send is a keyframe");
        assert_eq!(epochs, 0, "fresh instance stamps epoch 0");

        st.send_slot(0, &frame_with("ho"));
        with_test_host(|h| {
            assert_eq!(h.sent.len(), 2);
            assert_eq!(h.sent[1].payload[0] & FLAG_KEYFRAME, 0, "steady state is a delta");
            assert!(h.sent[1].payload.len() < KEYFRAME_BYTES);
        });
        assert!(st.slot_baseline_equals_packed(0));
    }

    #[test]
    fn rejected_delta_retries_as_keyframe_with_host_epoch_in_call() {
        reset_test_host();
        let mut st = SdkState::new();
        st.send_slot(0, &frame_with("hi")); // keyframe, epoch echo 0

        // Script a rejection: host returns epoch 7 for the delta...
        with_test_host(|h| h.epoch_script.push_back(7));
        // ...and echoes for the retry (no script → echo stamped epoch).
        st.send_slot(0, &frame_with("ho"));

        with_test_host(|h| {
            assert_eq!(h.sent.len(), 3, "delta + in-call keyframe retry");
            let delta = &h.sent[1].payload;
            let retry = &h.sent[2].payload;
            assert_eq!(delta[0] & FLAG_KEYFRAME, 0, "second send was a delta");
            assert_eq!(retry[0] & FLAG_KEYFRAME, FLAG_KEYFRAME, "retry is a keyframe");
            let retry_epoch = u32::from_le_bytes(retry[1..5].try_into().unwrap());
            assert_eq!(retry_epoch, 7, "retry stamped with the HOST's returned epoch");
        });
        assert_eq!(st.slot_state(0), (7, true), "slot adopted the host epoch");
    }

    #[test]
    fn identical_reconciles_every_player_slot() {
        reset_test_host();
        let mut st = SdkState::new();
        st.send_slot(0, &frame_with("p0 view"));
        st.send_slot(1, &frame_with("p1 view"));

        // Broadcast: every ALLOCATED per-index baseline must now equal the
        // broadcast frame (lazy contract: never-sent slots are NOT
        // materialized — they stay not-present and recover via their first
        // per-player send opening with a keyframe, mirroring the host).
        st.send_slot(BROADCAST_SLOT, &frame_with("everyone"));
        for slot in [0usize, 1] {
            assert!(st.slot_baseline_equals_packed(slot), "slot {slot} reconciled");
            assert!(st.slot_state(slot).1);
        }
        assert!(!st.slot_state(2).1, "never-sent slot must stay not-present");

        // A per-player send after the broadcast diffs against the broadcast
        // frame: it must be a small delta, not a keyframe.
        st.send_slot(1, &frame_with("everyone!"));
        with_test_host(|h| {
            let last = h.sent.last().unwrap();
            assert_eq!(last.player_idx, Some(1));
            assert_eq!(last.payload[0] & FLAG_KEYFRAME, 0, "post-broadcast send is a delta");
        });
    }

    #[test]
    fn roster_change_invalidates_all_baselines() {
        reset_test_host();
        let mut st = SdkState::new();
        let r1 = vec![player("a")];
        let r2 = vec![player("a"), player("b")];

        st.roster_gate(&r1);
        st.send_slot(0, &frame_with("x"));
        st.roster_gate(&r1); // same roster: baselines stay
        assert!(st.slot_state(0).1);

        st.roster_gate(&r2); // roster changed: every baseline invalid
        assert!(!st.slot_state(0).1);
        st.send_slot(0, &frame_with("y"));
        with_test_host(|h| {
            let last = h.sent.last().unwrap();
            assert_eq!(last.payload[0] & FLAG_KEYFRAME, FLAG_KEYFRAME, "post-roster-change send keyframes");
        });
    }

    #[test]
    fn out_of_range_slot_is_dropped() {
        reset_test_host();
        let mut st = SdkState::new();
        st.send_slot(SLOTS + 3, &frame_with("x")); // must not panic
        with_test_host(|h| assert!(h.sent.is_empty()));
    }
}
