package sdk

import (
	"context"
	"log/slog"
)

// Services is the per-room bundle of shared concerns, constructed by a
// ServicesFactory. Games reach shared concerns ONLY via Room.Services() and
// MUST drop the reference in OnClose.
type Services struct {
	Leaderboard LeaderboardClient
	Accounts    AccountStore
	Config      ConfigStore // slug-bound, read-only per-game config (may be nil)
	Credits     CreditsService
	Chat        ChatClient
	Spectate    SpectatorClient
	Log         *slog.Logger
}

// CreditsService is the host-side credits surface behind the credits host
// functions (casino-kind games). The implementation owns every rule: atomic
// escrow debits, gross-payout settlement under the declared-multiplier and
// platform clamps, refunds. May be nil (no economy): the host functions then
// report economy-disabled to the guest. Errors are mapped to the ABI status
// codes via the Err* sentinels in this package.
type CreditsService interface {
	// Balance reads the player's account-wide balance.
	Balance(ctx context.Context, p Player) (int64, error)
	// Wager atomically escrows amount from the player's balance into the
	// room-seat's open stake.
	Wager(ctx context.Context, p Player, amount int64) error
	// Settle closes the seat's open stake with the gross payout (0 = loss),
	// clamped by the implementation.
	Settle(ctx context.Context, p Player, payout int64) error
}

// ServicesFactory constructs a per-room Services. It has distinct
// implementations for production (durable) and dev (in-memory).
type ServicesFactory interface {
	// For builds the Services for a room, tagging the logger with room + slug.
	For(roomID, slug string) Services
}

// LeaderboardClient is the game-facing write side. Games ALWAYS call Post and
// never branch on eligibility; the implementation records every account-bound
// result tagged with mode + status (dropping only guests). Reads are NOT here —
// they live on LeaderboardReader, which games never receive.
type LeaderboardClient interface {
	Post(slug string, r Result)
}

// ChatClient is a room-local chat hook (no-op stub in v1).
type ChatClient interface {
	Broadcast(roomID, from, msg string)
}

// SpectatorClient is a read-only join hook (no-op stub in v1).
type SpectatorClient interface {
	Open(roomID string) error
}
