package sdk

import "log/slog"

// Services is the per-room bundle of shared concerns, constructed by a
// ServicesFactory. Games reach shared concerns ONLY via Room.Services() and
// MUST drop the reference in OnClose.
type Services struct {
	Leaderboard LeaderboardClient
	Accounts    AccountStore
	Config      ConfigStore // slug-bound, read-only per-game config (may be nil)
	Chat        ChatClient
	Spectate    SpectatorClient
	Log         *slog.Logger
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
