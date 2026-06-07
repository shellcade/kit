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

// Ctx member-section sentinels (roster-epoch mode, ABI.md §4.1 minor): real
// rosters are capped far below these values, so the count u16 disambiguates.
pub(crate) const CTX_ROSTER_UNCHANGED: u16 = 0xFFFF;
pub(crate) const CTX_ROSTER_FULL: u16 = 0xFFFE;

// ---- CallContext (§4.1) ------------------------------------------------------

/// The decoded per-callback room state. `members` is shared with the SDK's
/// roster cache (`Rc`): on the roster-epoch unchanged form the cached roster
/// is reused with zero member allocations.
pub(crate) struct CallCtx {
    pub now_unix_nanos: i64,
    pub cfg: RoomConfig,
    pub members: std::rc::Rc<Vec<Player>>,
    pub settled: bool,
    /// Roster-epoch mode: the epoch carried by a sentinel-form member
    /// section (None = legacy full-roster form).
    pub roster_epoch: Option<u32>,
    /// True when the sentinel said "unchanged" — `members` is left empty by
    /// the decoder and the caller resolves it from the cache.
    pub roster_unchanged: bool,
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
    let count = r.u16();
    let (members, roster_epoch, roster_unchanged) = match count {
        CTX_ROSTER_UNCHANGED => {
            // Sentinel: epoch only — the caller resolves members from the
            // SDK roster cache (zero member allocations here).
            let epoch = r.u32();
            (Vec::new(), Some(epoch), true)
        }
        CTX_ROSTER_FULL => {
            let epoch = r.u32();
            let n = r.u16() as usize;
            (decode_members(&mut r, n), Some(epoch), false)
        }
        n => (decode_members(&mut r, n as usize), None, false),
    };
    let settled = r.u8() != 0;
    (
        CallCtx {
            now_unix_nanos: now,
            cfg: RoomConfig { mode, capacity, min_players, seed, seed_set },
            members: std::rc::Rc::new(members),
            settled,
            roster_epoch,
            roster_unchanged,
        },
        r,
    )
}

fn decode_members(r: &mut Rd<'_>, n: usize) -> Vec<Player> {
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
    members
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
    // Trailing config-spec section (ABI.md §4.2, spec minor): always written,
    // count 0 when nothing is declared. Declarations are validated here so an
    // authoring mistake fails loudly at meta() time — the same fail-fast
    // posture as the Go SDK.
    if let Err(e) = validate_config_specs(m.config) {
        panic!("shellcade-kit: invalid Meta.config: {e}");
    }
    w.u16(m.config.len().min(0xffff) as u16);
    for cs in m.config {
        w.str(cs.key);
        w.str(cs.title);
        w.str(cs.description);
        w.u8(cs.config_type as u8);
        w.str(cs.default);
        w.str(cs.schema);
    }
    // Trailing large-room section (ABI.md §4.2, spec minor): ctx-features
    // bitset + declared heartbeat. Always written; validated here under the
    // same fail-fast posture as config specs.
    if let Err(e) = validate_meta_trailer(m.ctx_features, m.heartbeat_ms) {
        panic!("shellcade-kit: invalid Meta: {e}");
    }
    w.u32(m.ctx_features);
    w.u16(m.heartbeat_ms);
    // Trailing lifecycle byte (ABI.md §4.2, spec minor). Always written;
    // resident with min_players > 1 is an authoring bug (a resident room
    // runs with zero members), mirroring Go's wire.ValidateLifecycle.
    if m.lifecycle == crate::types::Lifecycle::Resident && m.min_players > 1 {
        panic!("shellcade-kit: invalid Meta: lifecycle Resident cannot require min_players {}", m.min_players);
    }
    w.u8(m.lifecycle as u8);
    w.b
}

/// The authoring rules for the large-room meta trailer, mirroring Go's
/// `wire.ValidateMetaTrailer`: no undefined ctx-feature bits; heartbeat 0 or
/// within the platform envelope.
pub(crate) fn validate_meta_trailer(ctx_features: u32, heartbeat_ms: u16) -> Result<(), String> {
    use crate::types::{HEARTBEAT_MAX_MS, HEARTBEAT_MIN_MS, KNOWN_CTX_FEATURES};
    let unknown = ctx_features & !KNOWN_CTX_FEATURES;
    if unknown != 0 {
        return Err(format!("ctx_features declares undefined bit(s) {unknown:#x}"));
    }
    if heartbeat_ms != 0 && !(HEARTBEAT_MIN_MS..=HEARTBEAT_MAX_MS).contains(&heartbeat_ms) {
        return Err(format!(
            "heartbeat_ms {heartbeat_ms} outside 0 or [{HEARTBEAT_MIN_MS},{HEARTBEAT_MAX_MS}]"
        ));
    }
    Ok(())
}

/// The authoring rules for declared config specs (ABI.md §4.2), mirroring Go's
/// `wire.ValidateConfigSpecs`: keys non-empty and unique, no reserved `host.`
/// prefix, and `schema` only on `Json`-typed keys where it must itself be
/// well-formed JSON. (The type code is total by construction in Rust.)
pub(crate) fn validate_config_specs(specs: &[crate::types::ConfigKeySpec]) -> Result<(), String> {
    use crate::types::ConfigType;
    for (i, cs) in specs.iter().enumerate() {
        if cs.key.is_empty() {
            return Err("config spec has an empty key".into());
        }
        if specs[..i].iter().any(|p| p.key == cs.key) {
            return Err(format!("duplicate config spec key {:?}", cs.key));
        }
        if cs.key.starts_with("host.") {
            return Err(format!("config spec key {:?} uses the reserved \"host.\" prefix", cs.key));
        }
        if !cs.schema.is_empty() {
            if cs.config_type != ConfigType::Json {
                return Err(format!("config spec {:?} declares a schema on a non-json type", cs.key));
            }
            if !crate::json::valid(cs.schema) {
                return Err(format!("config spec {:?} schema is not valid JSON", cs.key));
            }
        }
    }
    Ok(())
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
    fn meta_config_spec_wire_layout() {
        use crate::types::{ConfigKeySpec, ConfigType};
        let m = Meta {
            slug: "g",
            name: "G",
            short_description: "d",
            min_players: 1,
            max_players: 4,
            config: &[
                ConfigKeySpec {
                    key: "odds-variant",
                    title: "Odds variant",
                    description: "PAR sheet.",
                    config_type: ConfigType::Json,
                    default: r#"{"name":"Default"}"#,
                    schema: r#"{"type":"object"}"#,
                },
                ConfigKeySpec { key: "motd", title: "Banner", config_type: ConfigType::Text, ..ConfigKeySpec::DEFAULT },
            ],
            ..Meta::DEFAULT
        };
        let b = encode_meta(&m);
        let mut r = Rd::new(&b);
        // Skip the pre-section fields.
        for _ in 0..3 {
            r.string();
        }
        r.u16();
        r.u16();
        assert_eq!(r.u16(), 0); // tags
        for _ in 0..3 {
            r.string();
        }
        assert_eq!(r.u8(), 0); // no leaderboard
        // The trailing config-spec section.
        assert_eq!(r.u16(), 2);
        assert_eq!(r.string(), "odds-variant");
        assert_eq!(r.string(), "Odds variant");
        assert_eq!(r.string(), "PAR sheet.");
        assert_eq!(r.u8(), 3); // Json
        assert_eq!(r.string(), r#"{"name":"Default"}"#);
        assert_eq!(r.string(), r#"{"type":"object"}"#);
        assert_eq!(r.string(), "motd");
        assert_eq!(r.string(), "Banner");
        assert_eq!(r.string(), "");
        assert_eq!(r.u8(), 0); // Text
        assert_eq!(r.string(), "");
        assert_eq!(r.string(), "");
        assert!(!r.bad());
    }

    /// Byte-identity with the Go reference: the hex is the Go
    /// `wire.EncodeMeta` output for this exact declaration (regenerate with a
    /// throwaway Go test against kit/wire if the fixture changes).
    #[test]
    fn meta_config_spec_matches_go_golden() {
        use crate::types::{ConfigKeySpec, ConfigType, Direction, MetricFormat};
        let m = Meta {
            slug: "golden",
            name: "Golden",
            short_description: "golden fixture",
            min_players: 1,
            max_players: 4,
            tags: &["a", "b"],
            leaderboard: Some(Leaderboard {
                metric_label: "score",
                direction: Direction::LowerBetter,
                aggregation: crate::types::Aggregation::BestResult,
                format: MetricFormat::Duration,
            }),
            config: &[
                ConfigKeySpec {
                    key: "odds-variant",
                    title: "Odds variant",
                    description: "PAR sheet.",
                    config_type: ConfigType::Json,
                    default: r#"{"name":"Default"}"#,
                    schema: r#"{"type":"object"}"#,
                },
                ConfigKeySpec { key: "motd", title: "Banner", description: "Floor banner.", config_type: ConfigType::Text, ..ConfigKeySpec::DEFAULT },
            ],
            ..Meta::DEFAULT
        };
        let golden = "0600676f6c64656e0600476f6c64656e0e00676f6c64656e206669787475726501000400020001006101006200000000000001050073636f726501000202000c006f6464732d76617269616e740c004f6464732076617269616e740a005041522073686565742e0312007b226e616d65223a2244656661756c74227d11007b2274797065223a226f626a656374227d04006d6f7464060042616e6e65720d00466c6f6f722062616e6e65722e000000000000000000000000";
        let got: String = encode_meta(&m).iter().map(|b| format!("{b:02x}")).collect();
        assert_eq!(got, golden, "Rust meta encoding diverges from the Go golden");
    }

    #[test]
    fn config_spec_validation_rejects_authoring_mistakes() {
        use crate::types::{ConfigKeySpec, ConfigType};
        let cases: &[(&str, &[ConfigKeySpec])] = &[
            ("empty key", &[ConfigKeySpec::DEFAULT]),
            ("duplicate key", &[
                ConfigKeySpec { key: "k", ..ConfigKeySpec::DEFAULT },
                ConfigKeySpec { key: "k", ..ConfigKeySpec::DEFAULT },
            ]),
            ("reserved prefix", &[ConfigKeySpec { key: "host.heartbeat_ms", ..ConfigKeySpec::DEFAULT }]),
            ("schema on non-json", &[ConfigKeySpec { key: "k", schema: "{}", ..ConfigKeySpec::DEFAULT }]),
            ("schema not JSON", &[ConfigKeySpec {
                key: "k",
                config_type: ConfigType::Json,
                schema: "{nope",
                ..ConfigKeySpec::DEFAULT
            }]),
        ];
        for (name, specs) in cases {
            assert!(validate_config_specs(specs).is_err(), "want error: {name}");
        }
        assert!(validate_config_specs(&[]).is_ok());
        assert!(validate_config_specs(&[ConfigKeySpec {
            key: "odds-variant",
            config_type: ConfigType::Json,
            schema: r#"{"type":"object"}"#,
            ..ConfigKeySpec::DEFAULT
        }])
        .is_ok());
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

    /// Sentinel forms: full carries epoch + members; unchanged carries only
    /// the epoch and leaves the reader at the event extras.
    #[test]
    fn ctx_sentinel_forms_decode() {
        // Hand-build a full-form payload (host-side encoding lives in the
        // engine; the bytes are pinned by ABI.md §4.1).
        let mut w = Buf::new();
        w.i64(9); // now
        w.i64(7); // seed
        w.u8(1); // seed_set
        w.u8(0); // mode quick
        w.u16(1000); // capacity
        w.u16(1); // min players
        w.u16(CTX_ROSTER_FULL);
        w.u32(42); // epoch
        w.u16(1); // real count
        w.str("ada");
        w.str("a");
        w.str("c1");
        w.u8(1); // kind member
        w.u8(0); // settled
        w.u8(0xAB); // event extra
        let (ctx, mut r) = decode_ctx(&w.b);
        assert_eq!(ctx.roster_epoch, Some(42));
        assert!(!ctx.roster_unchanged);
        assert_eq!(ctx.members.len(), 1);
        assert_eq!(ctx.members[0].account_id, "a");
        assert_eq!(r.u8(), 0xAB, "event extras misaligned");

        let mut w = Buf::new();
        w.i64(9);
        w.i64(7);
        w.u8(1);
        w.u8(0);
        w.u16(1000);
        w.u16(1);
        w.u16(CTX_ROSTER_UNCHANGED);
        w.u32(43);
        w.u8(0); // settled
        w.u8(0xCD);
        let (ctx, mut r) = decode_ctx(&w.b);
        assert_eq!(ctx.roster_epoch, Some(43));
        assert!(ctx.roster_unchanged);
        assert!(ctx.members.is_empty());
        assert_eq!(r.u8(), 0xCD, "event extras misaligned");
    }

    #[test]
    fn meta_trailer_validation() {
        assert!(validate_meta_trailer(0, 0).is_ok());
        assert!(validate_meta_trailer(crate::types::CTX_FEAT_ROSTER_EPOCH, 100).is_ok());
        assert!(validate_meta_trailer(1 << 9, 0).is_err(), "undefined bit");
        assert!(validate_meta_trailer(0, 5).is_err(), "below envelope");
        assert!(validate_meta_trailer(0, 1500).is_err(), "above envelope");
    }

    /// The large-room trailer golden: Go `wire.EncodeMeta` output for a meta
    /// declaring the roster-epoch feature and a 100ms heartbeat.
    #[test]
    fn meta_trailer_matches_go_encoding() {
        let m = Meta {
            slug: "lr",
            name: "LR",
            short_description: "",
            min_players: 1,
            max_players: 1000,
            ctx_features: crate::types::CTX_FEAT_ROSTER_EPOCH,
            heartbeat_ms: 100,
            ..Meta::DEFAULT
        };
        let got: String = encode_meta(&m).iter().map(|b| format!("{b:02x}")).collect();
        // trailer = u32 1 LE + u16 100 LE = "01000000" + "6400"
        assert!(got.ends_with("000001000000640000"), "trailer bytes diverge from the Go encoding: ...{}", &got[got.len()-18..]);
    }
}

