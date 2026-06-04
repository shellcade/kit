// Package kit is the shellcade guest SDK: the authoring surface for wasm
// games targeting shellcade ABI v1. It is implemented purely from the ABI
// contract — it imports no shellcade private code — and mirrors the native
// sdk package's value types so a native game ports mechanically.
//
// A game implements Game + Handler and calls Run(game) from main(), plus the
// eight //go:export trampolines (see examples/pokies/main.go).
package game

// ABIVersion is the ABI major version this SDK targets.
const ABIVersion uint32 = 1

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
