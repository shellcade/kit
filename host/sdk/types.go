// Package sdk is the game engine boundary. A game implements a Handler (event
// callbacks); the engine owns the Room runtime and hands the game a Room handle
// to drive output. The lobby holds a RoomCtl. The game never sees channels, the
// actor goroutine, or any bubbletea type.
package sdk

import (
	"time"

	"github.com/shellcade/kit/v2/host/canvas"
)

// Frame and Cell are aliases of the render/canvas types — the SDK never
// redefines them, so a game cannot exceed the fixed 80x24 canvas.
type (
	Frame = canvas.Grid
	Cell  = canvas.Cell
	Style = canvas.Style
)

// Kind distinguishes a keyless guest from a member (an account holding ≥1
// credential — any mix of SSH keys and passkeys). It is computed from credential
// count, never stored; credential TYPE is not a property of the account.
type Kind string

const (
	KindGuest  Kind = "guest"
	KindMember Kind = "member"
)

// Character is the resolved player character (player-character capability):
// one single-cell glyph, resolved ink/bg RGB (palette IDs never cross this
// boundary), and the single-byte ASCII fallback for non-UTF8 sessions. It is
// value-comparable, populated by the host BEFORE the player is admitted, and
// FIXED for the life of the connection — the Character Builder is reachable
// only from the lobby, so a character change always arrives as a NEW Player
// on the next join.
type Character struct {
	Glyph            string // exactly one code point, width 1 everywhere
	InkR, InkG, InkB uint8
	BgR, BgG, BgB    uint8
	Fallback         byte // printable ASCII shown on non-UTF8 sessions
}

// Player is a value-comparable membership token. It is usable as a map key and
// carries NO Session / io.ReadWriter reference. A reconnect yields a NEW Player.
type Player struct {
	AccountID string
	Handle    string
	Kind      Kind
	// Conn uniquely identifies this CONNECTION, so two concurrent sessions of the
	// same account (e.g. the same SSH key opened in two terminals) are DISTINCT
	// memberships rather than colliding on one map key. It is an opaque token, not
	// a Session reference; Player remains value-comparable. AccountID/Handle/Kind
	// still identify the account for leaderboard and identity policy.
	Conn string
	// Character is the resolved player character, fixed per connection (see
	// Character). Value-comparable, so Player remains usable as a map key.
	Character Character
	// IsSynthetic marks a load-test player (an account minted via the synthetic
	// token). Host-side only — it labels metrics so synthetic load is separable
	// from real traffic; the game guest never sees it (add-loadtest-harness).
	IsSynthetic bool
}

// Guest reports whether the player is a keyless guest.
func (p Player) Guest() bool { return p.Kind == KindGuest }

// DisplayName is the handle with a "(guest)" marker for guests.
func (p Player) DisplayName() string {
	if p.Kind == KindGuest {
		return p.Handle + " (guest)"
	}
	return p.Handle
}

// InputKind distinguishes a printable rune from a named key.
type InputKind uint8

const (
	InputRune InputKind = iota
	InputKey
)

// Key is a named (non-printable) key.
type Key uint8

const (
	KeyNone Key = iota
	KeyEnter
	KeyBackspace
	KeyEsc
	KeyTab
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyCtrlC
)

// Input is the SDK-neutral input event, translated from the transport at the
// Session boundary. It is NEVER a bubbletea message.
type Input struct {
	Kind InputKind
	Rune rune
	Key  Key
}

// RuneInput builds a printable-rune input.
func RuneInput(r rune) Input { return Input{Kind: InputRune, Rune: r} }

// KeyInput builds a named-key input.
func KeyInput(k Key) Input { return Input{Kind: InputKey, Key: k} }

// Mode is the matchmaking + timing classifier. It is NOT eligibility policy.
type Mode string

const (
	ModeQuick   Mode = "quick"
	ModePrivate Mode = "private"
	ModeSolo    Mode = "solo"
)

// Status is a player's terminal outcome.
type Status string

const (
	StatusFinished Status = "finished"
	StatusDNF      Status = "dnf"
	StatusFlagged  Status = "flagged"
)

// RoomConfig carries the matchmaking/timing classifier and the reproducibility
// seed. Mode must never drive leaderboard eligibility (that lives in Services).
type RoomConfig struct {
	Mode       Mode
	Capacity   int
	MinPlayers int
	Seed       int64
	SeedSet    bool

	// Lifecycle is the room's resolved end-of-life mode (declaration ∧
	// grant, resolved by the matchmaker at construction). It is engine
	// state, not wire state: callbacks don't carry it and snapshots don't
	// record it — a restore re-resolves it from the game's meta and the
	// grant list.
	Lifecycle Lifecycle
}

// Lifecycle is a room's end-of-life mode (mirrors the kit declaration).
type Lifecycle uint8

const (
	LifecycleResumable Lifecycle = 0 // hibernate on abandon (default)
	LifecycleEphemeral Lifecycle = 1 // end + dispose after the abandon grace
	LifecycleResident  Lifecycle = 2 // granted singleton; ticks while empty
)

// GameMeta is static game metadata referenced by slug.
type GameMeta struct {
	Slug             string   `json:"slug"`
	Name             string   `json:"name"`
	ShortDescription string   `json:"shortDescription"`
	MinPlayers       int      `json:"minPlayers"`
	MaxPlayers       int      `json:"maxPlayers"`
	Tags             []string `json:"tags,omitempty"`

	// Optional per-game lobby mode labels. An empty value means "use the lobby's
	// game-agnostic default", so the lobby carries no game-specific wording.
	QuickModeLabel    string `json:"quickModeLabel,omitempty"`    // mode-picker label for Quick; "" -> "Quick match"
	SoloModeLabel     string `json:"soloModeLabel,omitempty"`     // mode-picker label for Solo;  "" -> "Solo practice"
	PrivateInviteLine string `json:"privateInviteLine,omitempty"` // trailing line in the private-create flash; "" -> "Play begins when a second player joins."

	// Leaderboard optionally declares how this game's board behaves (label,
	// direction, aggregation, format). Nil means the defaults (best single
	// result, higher is better, integer) — see ResolveLeaderboardSpec.
	Leaderboard *LeaderboardSpec `json:"leaderboard,omitempty"`

	// Config optionally declares the game's admin-settable config keys so the
	// admin Game settings area can render typed get/edit forms. Nil/empty
	// means no declared surface (the generic editor still works).
	Config []ConfigKeySpec `json:"config,omitempty"`

	// CtxFeatures is the game's declared negotiated-callback-encoding bitset
	// (wire.CtxFeat*; 0 = none). The host honors bits it implements and
	// ignores the rest.
	CtxFeatures uint32 `json:"ctxFeatures,omitempty"`

	// HeartbeatMS is the game's declared wake cadence in milliseconds
	// (0 = no declaration). Precedence at room creation: admin
	// host.heartbeat_ms config > this declaration > the platform default,
	// clamped to the host envelope.
	HeartbeatMS int `json:"heartbeatMS,omitempty"`

	// Lifecycle is the game's declared end-of-life shape (resumable 0 /
	// ephemeral 1 / resident 2). Resident takes effect only when the
	// platform grants it; an undeclared or unknown value reads as
	// resumable.
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`

	// WireRevision is the wire revision (wire.Revision) of the kit the
	// artifact was built against, stamped by the SDK encoders — the
	// mechanical anchor for the deploy-order rule. 0 = unknown (the meta
	// predates the field, kit ≤ v2.7.x). A value ABOVE the host's compiled-in
	// wire.Revision means the artifact assumes wire semantics this host does
	// not implement; the catalog warns when it sees one.
	WireRevision uint16 `json:"wireRevision,omitempty"`

	// Controls is the game's declared extra controls (decoded from the meta
	// payload's trailing declared-controls section): inputs beyond the
	// canonical vocabulary, each with a short display label, surfaced by
	// touch front ends as tappable affordances that send exactly the
	// declared input. Presentation metadata only — declarations change no
	// input interpretation. Nil/empty = none declared.
	Controls []ControlDecl `json:"controls,omitempty"`

	// Hidden is a HOST-SET flag (json:"-", never decoded from a guest's declared
	// meta) marking a game live-but-unlisted: it is registered and reachable by
	// exact slug (quick-match, direct entry, admin), but the lobby's player-facing
	// games menu omits it. The built-in load-test game uses this so real players
	// never land in bot rooms (add-loadtest-harness).
	Hidden bool `json:"-"`
}

// ControlDecl is one game-declared extra control: the exact input it sends
// (a printable rune or a named key) and a short display label.
type ControlDecl struct {
	Kind  InputKind `json:"kind"`           // InputRune or InputKey
	Rune  rune      `json:"rune,omitempty"` // the printable rune (Kind == InputRune)
	Key   Key       `json:"key,omitempty"`  // the named key (Kind == InputKey)
	Label string    `json:"label"`
}

// ConfigType tells the admin surface how a declared config value is edited
// and validated (mirrors the kit/wire type codes).
type ConfigType uint8

const (
	ConfigText   ConfigType = iota // single-line string
	ConfigNumber                   // decimal number
	ConfigBool                     // true/false
	ConfigJSON                     // JSON document (multiline / rich form)
)

// ConfigEntry is one stored per-game config row (key, opaque value, write
// provenance) — the admin "what is set" listing shape returned by the store.
type ConfigEntry struct {
	Key       string
	Value     []byte
	UpdatedAt time.Time
	UpdatedBy string
}

// ConfigKeySpec is one game-declared admin-settable config key (decoded from
// the meta payload's trailing config-spec section).
type ConfigKeySpec struct {
	Key         string     `json:"key"`                   // the ConfigStore key the game reads
	Title       string     `json:"title"`                 // short admin-facing label
	Description string     `json:"description,omitempty"` // one or two sentences for the admin screen
	Type        ConfigType `json:"type"`                  // how the value is edited/validated
	Default     string     `json:"default,omitempty"`     // value the game uses when unset ("" = not declared)
	Schema      string     `json:"schema,omitempty"`      // JSON Schema document (ConfigJSON only; "" = none)
}

// PlayerResult is one player's outcome in a settled room.
type PlayerResult struct {
	Player Player
	Metric int
	Rank   int
	Status Status
}

// Result is the room-level outcome. Rankings contains exactly one PlayerResult
// for every player that joined (the engine backfills dnf for omissions).
type Result struct {
	Mode     Mode
	Rankings []PlayerResult

	// RoundSeq is the host-assigned, room-scoped 1-based sequence of this
	// leaderboard post (the gameabi host stamps it; the counter survives
	// hibernation). The durable leaderboard derives an idempotent round id from
	// (roomID, RoundSeq), so a post-restore re-settle of the same round — or a
	// retried write — dedupes instead of double-counting. 0 means unassigned
	// (a poster without replay determinism); the writer falls back to a random
	// round id, which still dedupes its own retries.
	RoundSeq uint64
}

// Phase is the engine-published, lobby-visible game phase. The game owns the
// values (via Room.SetPhase); the engine derives Remaining at read time.
type Phase struct {
	Name      string
	Open      bool
	Deadline  time.Time
	Remaining time.Duration
	Settled   bool
	Result    *Result
}

// Snapshot is the frozen, read-only room state handed to OnFrame and to the
// BroadcastFunc closure. Per-viewer composition reads only this (plus the
// viewing Player), never live room state.
type Snapshot interface {
	Members() []Player
	Config() RoomConfig
	Now() time.Time
}
