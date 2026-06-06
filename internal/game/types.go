// Package kit is the shellcade guest SDK: the authoring surface for wasm
// games targeting shellcade ABI v2. It is implemented purely from the ABI
// contract — it imports no shellcade private code — and mirrors the native
// sdk package's value types so a native game ports mechanically.
//
// A game implements Game + Handler and calls Run(game) from main(), plus the
// eight //go:export trampolines (`shellcade-kit new` scaffolds exactly this).
package game

// ABIVersion is the ABI major version this SDK targets.
const ABIVersion uint32 = 2

// Kind distinguishes a keyless guest from a member account.
type Kind uint8

const (
	KindGuest Kind = iota
	KindMember
)

// Player is a value-comparable membership token (mirrors native sdk.Player).
type Player struct {
	AccountID string
	Handle    string
	Kind      Kind
	Conn      string
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
}

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
