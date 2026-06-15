// Package session defines the transport-agnostic Session boundary. Lobby and
// game code read keystrokes and write frames through a Session and never branch
// on which transport produced it.
package session

import (
	"io"

	"github.com/shellcade/kit/v2/host/sdk"
)

// ColorDepth is the session's color capability.
type ColorDepth uint8

const (
	ColorNone ColorDepth = iota
	Color16
	Color256
	ColorTrue
)

func (c ColorDepth) String() string {
	switch c {
	case ColorTrue:
		return "truecolor"
	case Color256:
		return "256"
	case Color16:
		return "16"
	default:
		return "none"
	}
}

// Caps are per-session capabilities derived from the SSH environment.
type Caps struct {
	ColorDepth ColorDepth
	UTF8       bool
	Mouse      bool
}

// Size is a terminal size.
type Size struct {
	Cols int
	Rows int
}

// Session is the unified transport boundary. The default window is 80x24 when
// unknown.
type Session interface {
	io.ReadWriter
	Identity() sdk.Player
	Window() (cols, rows int)
	WindowChanges() <-chan Size
	Capabilities() Caps
	// RemoteIP is the client's IP (host portion only), used for per-login audit
	// records. "?" when unknown.
	RemoteIP() string
	Close() error
}

// InputContextNotifier is an OPTIONAL capability a Session may implement when
// its transport renders a client-side control surface that adapts to the
// game's published input context (the web front door's touch deck). The SSH
// transport does not implement it. The lobby type-asserts for it and calls
// NotifyInputContext on game enter/leave and whenever the active game's
// context changes; the transport forwards it out-of-band (a JSON control
// frame), invisible to hub.Serve and the games. Contexts are the wire
// strings "nav", "command", and "text".
type InputContextNotifier interface {
	NotifyInputContext(ctx string)
}

// ControlItem is one tappable control surfaced on a client-side deck: a short
// display label and the literal bytes a tap sends on the terminal stream —
// indistinguishable from the corresponding keypress.
type ControlItem struct {
	Label string `json:"label"`
	Send  string `json:"send"`
}

// ControlsNotifier is an OPTIONAL capability, sibling to InputContextNotifier,
// for transports whose control surface can render per-game tappable controls
// (the web touch deck's chips). The SSH transport does not implement it. The
// lobby calls NotifyControls with the active game's declared controls on game
// enter and with nil on return to the lobby; the transport forwards the set
// out-of-band, invisible to hub.Serve and the games.
type ControlsNotifier interface {
	NotifyControls(items []ControlItem)
}

// PasskeyRegistrar is an OPTIONAL capability a Session may implement when its
// transport can run a WebAuthn registration ceremony out-of-band (the web /
// WebSocket front door). The SSH transport does not implement it. The lobby
// type-asserts for it to decide whether to offer the "Create Passkey" item, and
// invokes RegisterPasskey to run the ceremony — keeping all WebAuthn protocol
// confined to the transport, invisible to hub.Serve and the games.
type PasskeyRegistrar interface {
	// CanRegisterPasskey reports whether a passkey ceremony is actually available
	// (the transport is web AND WebAuthn is configured).
	CanRegisterPasskey() bool
	// RegisterPasskey runs the ceremony and, on success, returns the promoted
	// (member) player; the session's own Identity() also reflects it.
	RegisterPasskey() (sdk.Player, error)
}

// PasskeyAuthenticator is an OPTIONAL capability, sibling to PasskeyRegistrar,
// for transports that can run a discoverable-credential WebAuthn *login* ceremony
// out-of-band (the web / WebSocket front door). The SSH transport does not
// implement it. The lobby type-asserts for it to offer the "Log in with a
// passkey" item and invokes LoginPasskey to resolve a returning account and swap
// the live session's identity to it in place — no page reload.
type PasskeyAuthenticator interface {
	// CanLoginPasskey reports whether a login ceremony is available (the transport
	// is web AND WebAuthn is configured).
	CanLoginPasskey() bool
	// LoginPasskey runs the ceremony and, on success, returns the resolved account
	// player; the session's own Identity() also reflects it.
	LoginPasskey() (sdk.Player, error)
}
