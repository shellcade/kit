//! Wire codecs (ABI.md §4): the little-endian append encoder, the
//! bounds-checked decoder (short reads degrade to zero and set a `bad` flag the
//! dispatch layer gates on — degraded values are never delivered to a game),
//! CallContext decode, and the Meta / Result encoders. Target-agnostic.

use crate::types::{Kind, Meta, Mode, Outcome, Player, RoomConfig};

// ---- little-endian append encoder ------------------------------------------

pub(crate) struct Buf {
    pub b: Vec<u8>,
}

impl Buf {
    pub fn new() -> Self {
        Buf { b: Vec::new() }
    }
    pub fn u8(&mut self, v: u8) {
        self.b.push(v);
    }
    pub fn u16(&mut self, v: u16) {
        self.b.extend_from_slice(&v.to_le_bytes());
    }
    pub fn u32(&mut self, v: u32) {
        self.b.extend_from_slice(&v.to_le_bytes());
    }
    pub fn i64(&mut self, v: i64) {
        self.b.extend_from_slice(&v.to_le_bytes());
    }
    /// str: u16 length || UTF-8 bytes.
    pub fn str(&mut self, s: &str) {
        let bytes = s.as_bytes();
        let n = bytes.len().min(0xffff);
        self.u16(n as u16);
        self.b.extend_from_slice(&bytes[..n]);
    }
}

// ---- bounds-checked decoder --------------------------------------------------

/// Short reads degrade to zero values and latch `bad` — they never panic. The
/// dispatch layer checks [`Rd::bad`] after reading an export's trailing fields
/// and DROPS the callback rather than delivering degraded zeros. Trailing
/// bytes beyond the fields read are ignored (tolerant reader, ABI.md §5).
pub(crate) struct Rd<'a> {
    b: &'a [u8],
    off: usize,
    bad: bool,
}

impl<'a> Rd<'a> {
    pub fn new(b: &'a [u8]) -> Self {
        Rd { b, off: 0, bad: false }
    }
    /// Whether any read so far ran past the payload.
    pub fn bad(&self) -> bool {
        self.bad
    }
    fn take(&mut self, n: usize) -> Option<&'a [u8]> {
        if self.bad || self.off + n > self.b.len() {
            self.bad = true;
            return None;
        }
        let s = &self.b[self.off..self.off + n];
        self.off += n;
        Some(s)
    }
    pub fn u8(&mut self) -> u8 {
        self.take(1).map_or(0, |s| s[0])
    }
    pub fn u16(&mut self) -> u16 {
        self.take(2).map_or(0, |s| u16::from_le_bytes([s[0], s[1]]))
    }
    pub fn u32(&mut self) -> u32 {
        self.take(4).map_or(0, |s| u32::from_le_bytes([s[0], s[1], s[2], s[3]]))
    }
    pub fn i64(&mut self) -> i64 {
        self.take(8).map_or(0, |s| {
            let mut a = [0u8; 8];
            a.copy_from_slice(s);
            i64::from_le_bytes(a)
        })
    }
    pub fn string(&mut self) -> String {
        let n = self.u16() as usize;
        self.take(n).map_or(String::new(), |s| String::from_utf8_lossy(s).into_owned())
    }
}

// ---- CallContext (§4.1) ------------------------------------------------------

/// The decoded per-callback room state.
pub(crate) struct CallCtx {
    pub now_unix_nanos: i64,
    pub cfg: RoomConfig,
    pub members: Vec<Player>,
    pub settled: bool,
}

/// Decode the CallContext prefix and return it plus the reader positioned at
/// the trailing per-export args (e.g. playerIdx for join/leave/input).
pub(crate) fn decode_ctx(input: &[u8]) -> (CallCtx, Rd<'_>) {
    let mut r = Rd::new(input);
    let now = r.i64();
    let seed = r.i64();
    let seed_set = r.u8() != 0;
    let mode = match r.u8() {
        1 => Mode::Private,
        2 => Mode::Solo,
        _ => Mode::Quick,
    };
    let capacity = r.u16() as usize;
    let min_players = r.u16() as usize;
    let n = r.u16() as usize;
    let mut members = Vec::with_capacity(n.min(64));
    for _ in 0..n {
        let handle = r.string();
        let account_id = r.string();
        let conn = r.string();
        let kind = if r.u8() == 1 { Kind::Member } else { Kind::Guest };
        if r.bad() {
            break; // degrade: keep what decoded cleanly
        }
        members.push(Player { handle, account_id, conn, kind });
    }
    let settled = r.u8() != 0;
    (
        CallCtx {
            now_unix_nanos: now,
            cfg: RoomConfig { mode, capacity, min_players, seed, seed_set },
            members,
            settled,
        },
        r,
    )
}

// ---- Meta (§4.2) ---------------------------------------------------------------

/// Pack a [`Meta`] for the `meta` export — the single SDK-owned serializer.
pub(crate) fn encode_meta(m: &Meta) -> Vec<u8> {
    let mut w = Buf::new();
    w.str(m.slug);
    w.str(m.name);
    w.str(m.short_description);
    w.u16(m.min_players);
    w.u16(m.max_players);
    w.u16(m.tags.len().min(0xffff) as u16);
    for t in m.tags {
        w.str(t);
    }
    w.str(m.quick_mode_label);
    w.str(m.solo_mode_label);
    w.str(m.private_invite_line);
    match &m.leaderboard {
        None => w.u8(0),
        Some(lb) => {
            w.u8(1);
            w.str(lb.metric_label);
            w.u8(lb.direction as u8);
            w.u8(lb.aggregation as u8);
            w.u8(lb.format as u8);
        }
    }
    w.b
}

// ---- Result (§4.4) -------------------------------------------------------------

/// Pack an [`Outcome`] against the current roster (player → index; an absent
/// player degrades to index 0, mirroring the Go SDK).
pub(crate) fn encode_outcome(res: &Outcome, roster: &[Player]) -> Vec<u8> {
    let mut w = Buf::new();
    w.u16(res.rankings.len().min(0xffff) as u16);
    for pr in &res.rankings {
        let idx = roster.iter().position(|p| *p == pr.player).unwrap_or(0);
        w.u32(idx as u32);
        w.i64(pr.metric);
        w.u16(pr.rank);
        w.u8(pr.status as u8);
    }
    w.b
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::{Leaderboard, PlayerResult, Status};

    fn ctx_payload(members: &[(&str, &str, u8)], trailing: &[u8]) -> Vec<u8> {
        let mut w = Buf::new();
        w.i64(123_000_000); // now
        w.i64(42); // seed
        w.u8(1); // seedSet
        w.u8(0); // mode quick
        w.u16(2); // capacity
        w.u16(2); // minPlayers
        w.u16(members.len() as u16);
        for (handle, id, kind) in members {
            w.str(handle);
            w.str(id);
            w.str("conn-1");
            w.u8(*kind);
        }
        w.u8(0); // settled
        w.b.extend_from_slice(trailing);
        w.b
    }

    #[test]
    fn ctx_round_trip_and_trailing_args() {
        let payload = ctx_payload(&[("alice", "acct-a", 1), ("bob", "acct-b", 0)], &7u32.to_le_bytes());
        let (ctx, mut r) = decode_ctx(&payload);
        assert_eq!(ctx.now_unix_nanos, 123_000_000);
        assert_eq!(ctx.cfg.seed, 42);
        assert!(ctx.cfg.seed_set);
        assert_eq!(ctx.members.len(), 2);
        assert_eq!(ctx.members[0].handle, "alice");
        assert_eq!(ctx.members[0].kind, Kind::Member);
        assert!(ctx.members[1].guest());
        assert_eq!(r.u32(), 7);
        assert!(!r.bad());
    }

    #[test]
    fn short_read_degrades_and_latches_bad() {
        let payload = ctx_payload(&[("alice", "acct-a", 1)], &[]);
        let (_, mut r) = decode_ctx(&payload);
        assert_eq!(r.u32(), 0); // no trailing u32 → degrade to 0
        assert!(r.bad());
        assert_eq!(r.u32(), 0); // stays degraded
    }

    #[test]
    fn truncated_ctx_never_panics() {
        let full = ctx_payload(&[("alice", "acct-a", 1)], &[]);
        for n in 0..full.len() {
            let (_ctx, _r) = decode_ctx(&full[..n]); // must not panic
        }
    }

    #[test]
    fn meta_wire_layout() {
        let m = Meta {
            slug: "g",
            name: "G",
            short_description: "d",
            min_players: 1,
            max_players: 4,
            tags: &["a", "b"],
            leaderboard: Some(Leaderboard {
                metric_label: "score",
                direction: crate::types::Direction::LowerBetter,
                aggregation: crate::types::Aggregation::BestResult,
                format: crate::types::MetricFormat::Duration,
            }),
            ..Meta::DEFAULT
        };
        let b = encode_meta(&m);
        let mut r = Rd::new(&b);
        assert_eq!(r.string(), "g");
        assert_eq!(r.string(), "G");
        assert_eq!(r.string(), "d");
        assert_eq!(r.u16(), 1);
        assert_eq!(r.u16(), 4);
        assert_eq!(r.u16(), 2);
        assert_eq!(r.string(), "a");
        assert_eq!(r.string(), "b");
        assert_eq!(r.string(), ""); // quick label
        assert_eq!(r.string(), ""); // solo label
        assert_eq!(r.string(), ""); // invite line
        assert_eq!(r.u8(), 1); // hasLeaderboard
        assert_eq!(r.string(), "score");
        assert_eq!(r.u8(), 1); // LowerBetter
        assert_eq!(r.u8(), 0); // BestResult
        assert_eq!(r.u8(), 2); // Duration
        assert!(!r.bad());
    }

    #[test]
    fn outcome_maps_players_to_roster_indices() {
        let roster = vec![
            Player { handle: "a".into(), account_id: "ia".into(), conn: "c1".into(), kind: Kind::Member },
            Player { handle: "b".into(), account_id: "ib".into(), conn: "c2".into(), kind: Kind::Guest },
        ];
        let res = Outcome {
            rankings: vec![
                PlayerResult { player: roster[1].clone(), metric: 9, rank: 1, status: Status::Finished },
                PlayerResult { player: roster[0].clone(), metric: 3, rank: 2, status: Status::Dnf },
            ],
        };
        let b = encode_outcome(&res, &roster);
        let mut r = Rd::new(&b);
        assert_eq!(r.u16(), 2);
        assert_eq!(r.u32(), 1); // b is roster index 1
        assert_eq!(r.i64(), 9);
        assert_eq!(r.u16(), 1);
        assert_eq!(r.u8(), 0);
        assert_eq!(r.u32(), 0);
        assert_eq!(r.i64(), 3);
        assert_eq!(r.u16(), 2);
        assert_eq!(r.u8(), 1); // DNF
    }
}
