package session

// StartupIntentKind is the routing a session requested at connect time.
type StartupIntentKind uint8

const (
	// StartupQuickMatch routes straight into a quick match for Slug.
	StartupQuickMatch StartupIntentKind = iota
	// StartupJoin routes straight into the lobby named by Code.
	StartupJoin
	// StartupInvalid carries a human-readable Reason for an unparseable request;
	// the lobby shows it and falls through to the root menu.
	StartupInvalid
)

// StartupIntent is a transport-neutral routing hint a Session may carry when its
// transport let the client name a destination at connect time (the SSH command
// string). It is a hint only: the front door does not resolve the slug/code or
// contact the matchmaker — the lobby validates and routes it. A session with no
// intent (the common case, and every /ws session) behaves exactly as before.
type StartupIntent struct {
	Kind   StartupIntentKind
	Slug   string // StartupQuickMatch: the exact author/name game slug.
	Code   string // StartupJoin: the invite code.
	Reason string // StartupInvalid: why the request could not be parsed.
}

// StartupRouter is an OPTIONAL capability a Session implements when its transport
// carries a connect-time command (the SSH front door parses one from
// sess.Command()). The lobby type-asserts for it once at startup; a transport
// that never carries a command (the /ws mobile/web front door) does not
// implement it, so the lobby sees no intent and behaves as it does today.
type StartupRouter interface {
	// StartupIntent reports the parsed connect-time intent. ok is false when the
	// session carried no command (normal lobby).
	StartupIntent() (StartupIntent, bool)
}
