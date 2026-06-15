package sdk

import "context"

// ConfigStore is a durable, read-only per-game configuration surface, already
// namespaced to one game's slug (the binding supplies the slug; the game names
// only the key, so it can neither read nor write another game's config). Values
// are opaque to the platform — a game parses its own document. Mutation is not
// exposed here: config is written only through the lobby's admin-gated path. A
// game obtains a ConfigStore via Room.Services().Config.
type ConfigStore interface {
	// Get returns the value for key and whether it was present. A missing key
	// reads as not-found (ok=false) so the game can fall back to its compiled
	// default.
	Get(ctx context.Context, key string) ([]byte, bool, error)
}
