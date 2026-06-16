package sdk

import "context"

// MergeRule governs how one per-user KV key is reconciled when two accounts
// merge. The zero value is the empty string; callers SHOULD pass an explicit
// rule, and the storage layer treats an empty/unknown rule as MergeKeepWinner.
type MergeRule string

const (
	// MergeKeepWinner keeps the surviving account's value on a key collision
	// (the default) and moves a loser-only key to the winner unchanged.
	MergeKeepWinner MergeRule = "keep-winner"
	// MergeKeepLoser takes the merged-away account's value on a collision.
	MergeKeepLoser MergeRule = "keep-loser"
	// MergeSum writes the integer sum of the two values (both must be integers).
	MergeSum MergeRule = "sum"
	// MergeMax writes the integer maximum of the two values (both integers).
	MergeMax MergeRule = "max"
)

// KVStore is a durable per-user key/value store, already namespaced to one game
// and one account. Values are opaque to the platform; the MergeSum/MergeMax
// rules additionally require the value to be a base-10 integer. A game obtains a
// KVStore via Account.Store().
type KVStore interface {
	// Get returns the value for key and whether it was present.
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Set writes value for key, recording the merge rule that governs how the
	// key reconciles on a future account merge.
	Set(ctx context.Context, key string, value []byte, rule MergeRule) error
	// Delete removes key (a no-op if absent).
	Delete(ctx context.Context, key string) error
}

// Account is a live, account-scoped handle a game obtains for a Player. It
// exposes the account's identity plus a per-user KVStore namespaced to the
// calling game. It is distinct from Player (a value-comparable, per-connection
// membership token): Account is fetched on demand and is not a map key.
type Account interface {
	ID() string     // immutable account UUID
	Handle() string // current display handle
	Kind() Kind
	Store() KVStore // per-user KV, auto-namespaced to this game's slug
}

// AccountStore yields an Account for a Player, with the returned KVStore
// auto-namespaced to the game whose room owns this Services bundle. It is part
// of Services; games reach it via Room.Services().Accounts.
type AccountStore interface {
	For(p Player) Account
}
