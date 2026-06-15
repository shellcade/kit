package session

import "github.com/shellcade/kit/v2/host/sdk"

// SignOuter is an OPTIONAL capability a Session may implement when its transport
// holds a durable, revocable identity the user can drop in place — the web /
// WebSocket front door, whose identity is a signed guest cookie. The SSH
// transport does NOT implement it (its identity is the connection's key, nothing
// to sign out of). The lobby type-asserts for it to offer the "Sign out" item and
// invokes SignOut to revert the live session to a fresh guest and clear the cookie.
type SignOuter interface {
	// CanSignOut reports whether a sign-out is available (the transport is web with
	// a durable cookie configured).
	CanSignOut() bool
	// SignOut reverts the live session to a brand-new guest account, clears the
	// durable cookie (browser-side, via the same one-time-token mechanism passkey
	// login uses to SET it), and returns the new guest player; the session's own
	// Identity() also reflects it.
	SignOut() (sdk.Player, error)
}
