package game

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/shellcade/kit/v2/wire"
)

// Room is the authoring surface: local reads answered from the cached
// CallContext (zero host calls), effects via host functions. A Room handle is
// valid only inside the callback that received it.
type Room interface {
	// Local reads.
	Members() []Player
	Has(p Player) bool
	Count() int
	Config() RoomConfig
	Rand() *rand.Rand
	Now() time.Time
	Settled() bool

	// Effects (host calls).
	Send(p Player, f *Frame)
	Identical(f *Frame)
	SetInputContext(ctx InputContext)
	End(res Result)
	Post(res Result)
	Log(msg string)

	Services() Services
}

// Handler is the game's per-room behavior — the lean wasm surface (OnWake is
// the host heartbeat; there are no ticks, timers, or frame callbacks).
type Handler interface {
	OnStart(r Room)
	OnJoin(r Room, p Player)
	OnLeave(r Room, p Player)
	OnInput(r Room, p Player, in Input)
	OnWake(r Room)
	OnClose(r Room)
}

// Base supplies no-op defaults so a game overrides only what it needs.
type Base struct{}

func (Base) OnStart(Room)                {}
func (Base) OnJoin(Room, Player)         {}
func (Base) OnLeave(Room, Player)        {}
func (Base) OnInput(Room, Player, Input) {}
func (Base) OnWake(Room)                 {}
func (Base) OnClose(Room)                {}

// Game is the module entry: static metadata plus the room behavior factory.
type Game interface {
	Meta() GameMeta
	NewRoom(cfg RoomConfig, svc Services) Handler
}

// ---- Services ----------------------------------------------------------------

// KVStore is the durable per-user KV, already namespaced to this game and one
// account (the HOST derives both — a guest can never name another namespace).
// Signatures mirror the native sdk so KV patterns port unchanged; the context
// is accepted and ignored (the host bounds the call).
type KVStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, rule MergeRule) error
	Delete(ctx context.Context, key string) error
}

// Account is a live, account-scoped handle for a Player.
type Account interface {
	ID() string
	Handle() string
	Kind() Kind
	Store() KVStore
}

// AccountStore yields an Account for a Player.
type AccountStore interface{ For(p Player) Account }

// ConfigStore is the slug-bound, read-only per-game config surface.
type ConfigStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
}

// Credits is the account-wide platform credits surface for casino-kind games
// (GameKindCasino; declare CtxFeatCredits). The host owns every balance: a
// Wager atomically escrows credits from the player's account-wide balance
// into the seat's open stake, and Settle closes the stake with a GROSS
// (stake-inclusive) payout — a loss settles 0, a push settles the stake, a
// win settles stake plus winnings; the host clamps the payout to stake x the
// game's declared MaxPayoutMultiplier. Repeated wagers before settlement
// accumulate one open stake (double-down, side bets). The game persists no
// balance of its own. Game-kind guests calling these get ErrCreditsDenied.
type Credits interface {
	// Balance reads the player's current account-wide credits balance.
	Balance(p Player) (int64, error)
	// Wager escrows amount from the player's balance into the seat's open
	// stake. ErrInsufficientCredits when the balance (or a platform bet
	// limit) refuses it — render it; the bet did not happen.
	Wager(p Player, amount int64) error
	// Settle closes the seat's open stake with the gross payout (0 = loss).
	Settle(p Player, payout int64) error
}

// Credits errors, mirrored from the ABI status codes. ErrEconomyDisabled
// means the host has the economy switched off — render an out-of-service
// state, never trap.
var (
	ErrInsufficientCredits = errors.New("kit: insufficient credits")
	ErrEconomyDisabled     = errors.New("kit: the credits economy is disabled on this host")
	ErrCreditsDenied       = errors.New("kit: credits are not available to this game or seat")
	ErrCreditsUnavailable  = errors.New("kit: credits are temporarily unavailable")
)

// creditsErr maps a wire status code to the typed error (nil for >= 0).
func creditsErr(code int64) error {
	switch {
	case code >= 0:
		return nil
	case code == wire.CreditsErrInsufficient:
		return ErrInsufficientCredits
	case code == wire.CreditsErrDisabled:
		return ErrEconomyDisabled
	case code == wire.CreditsErrDenied:
		return ErrCreditsDenied
	default:
		return ErrCreditsUnavailable
	}
}

// Services is the ABI v1 service bundle (no chat, no spectate).
type Services struct {
	Accounts AccountStore
	Config   ConfigStore

	// Credits is the platform credits surface (casino-kind games only; nil
	// on hosts/harnesses without an economy).
	Credits Credits
}
