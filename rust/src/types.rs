//! The authoring value types, mirroring the Go SDK's (`Player`, `RoomConfig`,
//! `Meta`, results, KV merge rules). Same-named where Rust conventions allow;
//! the Go↔Rust mapping notes live on each item.

/// Distinguishes a keyless guest from a member account.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum Kind {
    #[default]
    Guest,
    Member,
}

/// A value-comparable membership token (mirrors Go `kit.Player`).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct Player {
    pub account_id: String,
    pub handle: String,
    pub kind: Kind,
    pub conn: String,
}

impl Player {
    /// Whether the player is a keyless guest.
    pub fn guest(&self) -> bool {
        self.kind == Kind::Guest
    }

    /// The handle with a `"(guest)"` marker for guests.
    pub fn display_name(&self) -> String {
        if self.guest() {
            format!("{} (guest)", self.handle)
        } else {
            self.handle.clone()
        }
    }
}

/// The matchmaking + timing classifier.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum Mode {
    #[default]
    Quick,
    Private,
    Solo,
}

/// Room configuration decoded from the CallContext (mirrors Go `RoomConfig`).
#[derive(Clone, Debug, Default)]
pub struct RoomConfig {
    pub mode: Mode,
    pub capacity: usize,
    pub min_players: usize,
    pub seed: i64,
    pub seed_set: bool,
}

/// Governs per-user KV reconciliation on account merge.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum MergeRule {
    KeepWinner,
    KeepLoser,
    /// Value MUST be a base-10 ASCII int64 (unparsable degrades to keep-winner).
    Sum,
    /// Value MUST be a base-10 ASCII int64 (unparsable degrades to keep-winner).
    Max,
}

impl MergeRule {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            MergeRule::KeepWinner => "keep-winner",
            MergeRule::KeepLoser => "keep-loser",
            MergeRule::Sum => "sum",
            MergeRule::Max => "max",
        }
    }
}

/// A player's terminal outcome.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum Status {
    #[default]
    Finished,
    Dnf,
}

/// One player's outcome in a settled room (mirrors Go `PlayerResult`).
#[derive(Clone, Debug)]
pub struct PlayerResult {
    pub player: Player,
    pub metric: i64,
    pub rank: u16,
    pub status: Status,
}

/// The room-level outcome passed to `Room::end` / `Room::post`.
///
/// Go↔Rust note: this is Go's `kit.Result` — renamed because `Result` in a
/// Rust prelude would shadow `core::Result` in every game that glob-imports.
#[derive(Clone, Debug, Default)]
pub struct Outcome {
    pub rankings: Vec<PlayerResult>,
}

/// Leaderboard metric direction.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum Direction {
    #[default]
    HigherBetter,
    LowerBetter,
}

/// Leaderboard aggregation across a player's results.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum Aggregation {
    #[default]
    BestResult,
    SumResults,
}

/// Leaderboard metric display format.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum MetricFormat {
    #[default]
    Integer,
    Decimal,
    Duration,
}

/// Declares how a game's leaderboard behaves (mirrors Go `LeaderboardSpec`).
#[derive(Clone, Copy, Debug)]
pub struct Leaderboard {
    pub metric_label: &'static str,
    pub direction: Direction,
    pub aggregation: Aggregation,
    pub format: MetricFormat,
}

/// Config value type for a declared config key (wire codes; ABI.md §4.2).
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum ConfigType {
    /// Single-line string.
    #[default]
    Text,
    /// Decimal number.
    Number,
    /// true/false.
    Bool,
    /// JSON document (multiline / rich-form editing).
    Json,
}

/// One declared admin-settable config key (mirrors Go `ConfigKeySpec`): the
/// keys the game reads via `Room::config` declared in [`Meta::config`] so the
/// arcade's admin tools can render typed get/edit forms. Const-constructible
/// via `..ConfigKeySpec::DEFAULT`.
#[derive(Clone, Copy, Debug)]
pub struct ConfigKeySpec {
    /// The config key the game reads. Non-empty, unique, never `host.*`.
    pub key: &'static str,
    /// Short admin-facing label.
    pub title: &'static str,
    /// One or two sentences for the admin screen.
    pub description: &'static str,
    /// How the value is edited/validated (`type` on the wire).
    pub config_type: ConfigType,
    /// Value the game uses when unset (`""` = not declared).
    pub default: &'static str,
    /// JSON Schema document (`Json` keys only; `""` = none).
    pub schema: &'static str,
}

impl ConfigKeySpec {
    /// The all-defaults spec for `..ConfigKeySpec::DEFAULT` struct updates.
    pub const DEFAULT: ConfigKeySpec = ConfigKeySpec {
        key: "",
        title: "",
        description: "",
        config_type: ConfigType::Text,
        default: "",
        schema: "",
    };
}

/// Static game metadata (Go's `GameMeta`; the SDK owns the §4.2 serializer so
/// authors never write positional codec calls). Const-constructible:
///
/// ```
/// use shellcade_kit::Meta;
/// const META: Meta = Meta {
///     slug: "my-game",
///     name: "My Game",
///     short_description: "One line for the lobby.",
///     min_players: 1,
///     max_players: 2,
///     tags: &["solo", "puzzle"],
///     ..Meta::DEFAULT
/// };
/// ```
#[derive(Clone, Copy, Debug)]
pub struct Meta {
    /// Must be non-empty and equal the catalog directory name.
    pub slug: &'static str,
    pub name: &'static str,
    pub short_description: &'static str,
    /// Wire-width is u16 (ABI.md §4.2).
    pub min_players: u16,
    pub max_players: u16,
    pub tags: &'static [&'static str],
    /// `""` = platform default label.
    pub quick_mode_label: &'static str,
    pub solo_mode_label: &'static str,
    pub private_invite_line: &'static str,
    pub leaderboard: Option<Leaderboard>,
    /// Declared admin-settable config keys (`&[]` = none declared). Validated
    /// at `meta()` encode time — an invalid declaration is an authoring bug
    /// and panics there.
    pub config: &'static [ConfigKeySpec],
}

impl Meta {
    /// The all-defaults Meta for `..Meta::DEFAULT` struct updates
    /// (`..Default::default()` is not usable in const context).
    pub const DEFAULT: Meta = Meta {
        slug: "",
        name: "",
        short_description: "",
        min_players: 1,
        max_players: 1,
        tags: &[],
        quick_mode_label: "",
        solo_mode_label: "",
        private_invite_line: "",
        leaderboard: None,
        config: &[],
    };
}

impl Default for Meta {
    fn default() -> Self {
        Meta::DEFAULT
    }
}
