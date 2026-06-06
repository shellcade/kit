// Package kit is the shellcade game developer kit: the authoring surface
// for wasm games targeting the shellcade ABI (see ABI.md; the wire package is
// the ABI's code form).
//
// A game implements Game + Handler and calls Main(game) from main(), plus the
// eight //go:export trampolines for the wasm build — run `gamekit new` for a
// working scaffold, and see GUIDE.md for the full authoring guide.
//
// This package is a curated facade over internal/game; the implementation is
// internal so the public surface stays deliberate and versionable.
package kit

import "github.com/shellcade/kit/v2/internal/game"

// ABIVersion is the ABI major version this SDK targets.
const ABIVersion = game.ABIVersion

// ---- players & inputs ----------------------------------------------------------

type (
	Player       = game.Player
	Kind         = game.Kind
	Input        = game.Input
	InputKind    = game.InputKind
	Key          = game.Key
	InputContext = game.InputContext
	Action       = game.Action
)

const (
	KindGuest  = game.KindGuest
	KindMember = game.KindMember

	InputRune = game.InputRune
	InputKey  = game.InputKey

	KeyNone      = game.KeyNone
	KeyEnter     = game.KeyEnter
	KeyBackspace = game.KeyBackspace
	KeyEsc       = game.KeyEsc
	KeyTab       = game.KeyTab
	KeyUp        = game.KeyUp
	KeyDown      = game.KeyDown
	KeyLeft      = game.KeyLeft
	KeyRight     = game.KeyRight
	KeyCtrlC     = game.KeyCtrlC

	CtxNav     = game.CtxNav
	CtxCommand = game.CtxCommand
	CtxText    = game.CtxText

	ActNone    = game.ActNone
	ActUp      = game.ActUp
	ActDown    = game.ActDown
	ActLeft    = game.ActLeft
	ActRight   = game.ActRight
	ActConfirm = game.ActConfirm
	ActBack    = game.ActBack
)

// Resolve maps an Input to a semantic Action for the given context — the
// platform's canonical control vocabulary, reimplemented locally.
func Resolve(in Input, ctx InputContext) Action { return game.Resolve(in, ctx) }

// ---- rooms & results -------------------------------------------------------------

type (
	RoomConfig      = game.RoomConfig
	Mode            = game.Mode
	MergeRule       = game.MergeRule
	GameMeta        = game.GameMeta
	LeaderboardSpec = game.LeaderboardSpec
	ConfigKeySpec   = game.ConfigKeySpec
	ConfigType      = game.ConfigType
	Direction       = game.Direction
	Aggregation     = game.Aggregation
	MetricFormat    = game.MetricFormat
	Status          = game.Status
	PlayerResult    = game.PlayerResult
	Result          = game.Result
)

const (
	ModeQuick   = game.ModeQuick
	ModePrivate = game.ModePrivate
	ModeSolo    = game.ModeSolo

	MergeKeepWinner = game.MergeKeepWinner
	MergeKeepLoser  = game.MergeKeepLoser
	MergeSum        = game.MergeSum
	MergeMax        = game.MergeMax

	HigherBetter = game.HigherBetter
	LowerBetter  = game.LowerBetter

	ConfigText   = game.ConfigText
	ConfigNumber = game.ConfigNumber
	ConfigBool   = game.ConfigBool
	ConfigJSON   = game.ConfigJSON
	BestResult   = game.BestResult
	SumResults   = game.SumResults
	Integer      = game.Integer
	Decimal      = game.Decimal
	Duration     = game.Duration

	StatusFinished = game.StatusFinished
	StatusDNF      = game.StatusDNF
	StatusFlagged  = game.StatusFlagged
)

// ---- the canvas -------------------------------------------------------------------

type (
	Frame = game.Frame
	Cell  = game.Cell
	Style = game.Style
	Color = game.Color
	Attr  = game.Attr
)

const (
	Rows = game.Rows
	Cols = game.Cols

	AttrBold      = game.AttrBold
	AttrDim       = game.AttrDim
	AttrUnderline = game.AttrUnderline
	AttrReverse   = game.AttrReverse
)

// RGB constructs a truecolor value; Gray an even gray.
func RGB(r, g, b uint8) Color { return game.RGB(r, g, b) }
func Gray(v uint8) Color      { return game.Gray(v) }

// Standard palette.
var (
	White   = game.White
	Red     = game.Red
	Green   = game.Green
	Yellow  = game.Yellow
	Cyan    = game.Cyan
	DimGray = game.DimGray
)

// NewFrame returns a blank 24x80 frame. Frames are handled by POINTER
// throughout the SDK (see ABI.md §6).
func NewFrame() *Frame { return game.NewFrame() }

// ---- the authoring contract --------------------------------------------------------

type (
	Game         = game.Game
	Handler      = game.Handler
	Base         = game.Base
	Room         = game.Room
	Services     = game.Services
	KVStore      = game.KVStore
	Account      = game.Account
	AccountStore = game.AccountStore
	ConfigStore  = game.ConfigStore
)

// (Frame).Clear resets a frame for reuse — prefer one long-lived frame plus
// Clear() per render over NewFrame() per render (allocation-free steady state).
//
// Frame authoring methods (on *Frame, surfaced via the type alias above):
// Set/SetRune/Text/SetWide/Fill are unchanged single-code-point writers, and
// v2 adds (*Frame).SetGrapheme(row, col, cluster, style) and the width-2
// (*Frame).SetGraphemeWide(...) for clusters of up to three code points (VS16,
// skin-tone, keycap). Both refuse a >3- or 0-code-point cluster by drawing
// nothing (see GUIDE.md "Grapheme glyphs").
