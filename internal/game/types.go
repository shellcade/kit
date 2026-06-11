// Package kit is the shellcade guest SDK: the authoring surface for wasm
// games targeting shellcade ABI v2. It is implemented purely from the ABI
// contract — it imports no shellcade private code — and mirrors the native
// sdk package's value types so a native game ports mechanically.
//
// A game implements Game + Handler and calls Run(game) from main(), plus the
// eight //go:export trampolines (`shellcade-kit new` scaffolds exactly this).
package game

import "github.com/shellcade/kit/v2/wire"

// ABIVersion is the ABI major version this SDK targets.
const ABIVersion uint32 = 2

// Kind distinguishes a keyless guest from a member account.
type Kind uint8

const (
	KindGuest Kind = iota
	KindMember
)

// Character is a player's resolved arcade character (mirrors wire.Character):
// a single width-1 glyph with ink/background colors and an ASCII fallback
// codepoint. The zero value means "no character" — what every member carries
// unless the game declares CtxFeatCharacter in GameMeta.CtxFeatures.
type Character struct {
	Glyph    string
	InkR     uint8
	InkG     uint8
	InkB     uint8
	BgR      uint8
	BgG      uint8
	BgB      uint8
	Fallback uint8
}

// Player is a value-comparable membership token (mirrors native sdk.Player).
type Player struct {
	AccountID string
	Handle    string
	Kind      Kind
	Conn      string
	Character Character
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

// Key is a named (non-printable) key. Values match the native sdk.
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

// Input is the SDK-neutral input event.
type Input struct {
	Kind InputKind
	Rune rune
	Key  Key
}

// knownInput reports whether an input's kind (and, for a named key, its key
// value) is one this SDK version understands. The v2 tolerant-reader rule
// requires unknown kind/key values to be ignored rather than faulting — future
// input growth (mouse, paste, focus, new named keys) is then an additive minor.
func knownInput(in Input) bool {
	switch in.Kind {
	case InputRune:
		return true
	case InputKey:
		return in.Key <= KeyCtrlC // KeyNone..KeyCtrlC are the assigned keys
	default:
		return false
	}
}

// Mode is the matchmaking + timing classifier.
type Mode uint8

const (
	ModeQuick Mode = iota
	ModePrivate
	ModeSolo
)

// RoomConfig mirrors the native sdk.RoomConfig.
type RoomConfig struct {
	Mode       Mode
	Capacity   int
	MinPlayers int
	Seed       int64
	SeedSet    bool
}

// MergeRule governs per-user KV reconciliation on account merge.
type MergeRule string

const (
	MergeKeepWinner MergeRule = "keep-winner"
	MergeKeepLoser  MergeRule = "keep-loser"
	MergeSum        MergeRule = "sum"
	MergeMax        MergeRule = "max"
)

// Leaderboard enums (values match the native sdk's uint8 enums).
type (
	Direction    uint8
	Aggregation  uint8
	MetricFormat uint8
)

const (
	HigherBetter Direction = iota
	LowerBetter
)
const (
	BestResult Aggregation = iota
	SumResults
)
const (
	Integer MetricFormat = iota
	Decimal
	Duration
)

// LeaderboardSpec declares how a game's board behaves.
type LeaderboardSpec struct {
	MetricLabel string
	Direction   Direction
	Aggregation Aggregation
	Format      MetricFormat
}

// ConfigType tells the platform's admin surface how to render and validate a
// declared config value (values match the wire type codes).
type ConfigType uint8

const (
	ConfigText   ConfigType = iota // single-line string
	ConfigNumber                   // decimal number
	ConfigBool                     // true/false
	ConfigJSON                     // JSON document (multiline / rich form)
)

// ConfigKeySpec declares one admin-settable config key the game reads via
// Services.Config. Declaring specs is optional; they exist so the platform's
// admin tools can render a real get/edit surface for the game's keys.
type ConfigKeySpec struct {
	Key         string     // the ConfigStore key the game reads
	Title       string     // short admin-facing label
	Description string     // one or two sentences for the admin screen
	Type        ConfigType // how the value is edited/validated
	Default     string     // value the game uses when unset ("" = not declared)
	Schema      string     // JSON Schema document (ConfigJSON only; "" = none)
}

// GameMeta is the static game metadata (mirrors native sdk.GameMeta).
type GameMeta struct {
	Slug             string
	Name             string
	ShortDescription string
	MinPlayers       int
	MaxPlayers       int
	Tags             []string

	QuickModeLabel    string
	SoloModeLabel     string
	PrivateInviteLine string

	Leaderboard *LeaderboardSpec

	// Config optionally declares the game's admin-settable config keys.
	// Nil/empty means the game declares no config surface (the platform's
	// generic editor still works). Declarations are validated at meta encode
	// time — an invalid spec list is an authoring bug and panics there.
	Config []ConfigKeySpec

	// CtxFeatures optionally opts the game into negotiated callback
	// encodings (the CtxFeat* bits; zero = none, today's behavior).
	// Undefined bits are an authoring bug and panic at meta encode time.
	CtxFeatures uint32

	// HeartbeatMS optionally declares the game's preferred wake cadence in
	// milliseconds. 0 = no declaration (platform default). The host clamps
	// to its envelope and an admin config override always wins; out-of-range
	// declarations are an authoring bug and panic at meta encode time.
	HeartbeatMS int

	// Lifecycle optionally declares the room's end-of-life shape. The zero
	// value (LifecycleResumable) is today's behavior: hibernate on abandon,
	// player-driven resume. LifecycleEphemeral ends and disposes the room
	// after the abandon grace (no snapshot, no Resume entry) — right for
	// casual social rooms. LifecycleResident declares one long-lived room
	// per slug; it takes effect only when the platform grants it (an
	// ungranted declaration behaves as resumable). Undefined values and
	// resident-with-MinPlayers>1 are authoring bugs and panic at meta
	// encode time.
	Lifecycle Lifecycle

	// Controls optionally declares the game's extra controls: inputs beyond
	// the canonical vocabulary (a raw rune like 'r', or a named key like
	// KeyBackspace), each with a short display label. Front ends on devices
	// without the corresponding physical key (touch) surface each
	// declaration as a tappable affordance that sends exactly the declared
	// input — presentation metadata only; declarations change no input
	// interpretation. Nil/empty means no declarations, and a game fully
	// served by the canonical vocabulary needs none. Invalid declarations
	// are an authoring bug and panic at meta encode time.
	Controls []ControlDecl
}

// ControlDecl declares one extra control: the exact Input it sends (a
// printable rune or a named key) and a short display label of at most 16
// runes. Build with RuneControl / KeyControl.
type ControlDecl struct {
	Input Input
	Label string
}

// RuneControl declares a printable-rune control, e.g. RuneControl('r', "RESIGN").
func RuneControl(r rune, label string) ControlDecl {
	return ControlDecl{Input: Input{Kind: InputRune, Rune: r}, Label: label}
}

// KeyControl declares a named-key control, e.g. KeyControl(KeyBackspace, "UNDO").
func KeyControl(k Key, label string) ControlDecl {
	return ControlDecl{Input: Input{Kind: InputKey, Key: k}, Label: label}
}

// Lifecycle is the room end-of-life declaration.
type Lifecycle uint8

const (
	LifecycleResumable Lifecycle = Lifecycle(wire.LifecycleResumable)
	LifecycleEphemeral Lifecycle = Lifecycle(wire.LifecycleEphemeral)
	LifecycleResident  Lifecycle = Lifecycle(wire.LifecycleResident)
)

// CtxFeatRosterEpoch opts the game into the ctx roster-epoch encoding: the
// host sends the full member list only when the roster changes (with an
// epoch), and a 6-byte unchanged marker otherwise — the large-room callback
// path. Declare it in GameMeta.CtxFeatures.
const CtxFeatRosterEpoch = wire.CtxFeatRosterEpoch

// CtxFeatCharacter opts the game into per-member character sections: the host
// appends each member's resolved Character to every member-bearing roster
// encoding, and the SDK populates Player.Character. Without the declaration
// Player.Character is always the zero value. Declare it in
// GameMeta.CtxFeatures.
const CtxFeatCharacter = wire.CtxFeatCharacter

// Status is a player's terminal outcome.
type Status uint8

const (
	StatusFinished Status = iota
	StatusDNF
	StatusFlagged
)

// PlayerResult is one player's outcome in a settled room.
type PlayerResult struct {
	Player Player
	Metric int
	Rank   int
	Status Status
}

// Result is the room-level outcome.
type Result struct {
	Rankings []PlayerResult
}

// InputContext selects how an Input is interpreted (mirrors native sdk).
type InputContext uint8

const (
	CtxNav InputContext = iota
	CtxCommand
	CtxText
)

// Action is a resolved, semantic input action.
type Action uint8

const (
	ActNone Action = iota
	ActUp
	ActDown
	ActLeft
	ActRight
	ActConfirm
	ActBack
)

// Resolve maps an Input to a semantic Action for the given context. It is a
// local reimplementation of the platform's canonical control vocabulary:
//
//	Up=↑/k  Down=↓/j  Left=←/h  Right=→/l  Confirm=Enter/Space  Back=Esc/q/Ctrl-C
func Resolve(in Input, ctx InputContext) Action {
	if in.Kind == InputKey && (in.Key == KeyEsc || in.Key == KeyCtrlC) {
		return ActBack
	}
	if ctx == CtxText {
		return ActNone
	}
	if in.Kind == InputKey {
		switch in.Key {
		case KeyUp:
			return ActUp
		case KeyDown:
			return ActDown
		case KeyLeft:
			return ActLeft
		case KeyRight:
			return ActRight
		case KeyEnter:
			return ActConfirm
		}
		return ActNone
	}
	switch in.Rune {
	case ' ':
		return ActConfirm
	case 'q':
		return ActBack
	}
	if ctx == CtxNav {
		switch in.Rune {
		case 'k':
			return ActUp
		case 'j':
			return ActDown
		case 'h':
			return ActLeft
		case 'l':
			return ActRight
		}
	}
	return ActNone
}
