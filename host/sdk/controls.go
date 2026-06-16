package sdk

// Action is a resolved, semantic input action. It is the canonical vocabulary
// every lobby screen and game interprets, decoupled from the raw key or rune.
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

// InputContext selects how an Input is interpreted. It governs whether the
// mobile-friendly letter aliases (j/k/h/l, q) are active or whether the runes
// are literal input the caller handles itself.
type InputContext uint8

const (
	// CtxNav is for menus and list/value screens: all letter aliases are active.
	// It is the zero value, so a room defaults to Nav until a game says otherwise.
	CtxNav InputContext = iota
	// CtxCommand is for screens whose letters are domain commands (e.g. blackjack
	// h/s/d/p/r): arrows still navigate, q still backs out, but h/j/k/l are NOT
	// directions — the caller reads in.Rune for its command.
	CtxCommand
	// CtxText is for typing screens: only Esc/Ctrl-C resolve (to Back); every
	// other input, including q/j/k and printable runes, is ActNone and the caller
	// reads the raw Input.
	CtxText
)

// Resolve maps an Input to a semantic Action for the given context. It is the
// single source of truth for the canonical control vocabulary:
//
//	Up=↑/k  Down=↓/j  Left=←/h  Right=→/l  Confirm=Enter/Space  Back=Esc/q/Ctrl-C
//
// The letter aliases are active only where letters are not literal input; q is
// Back in every context except CtxText.
func Resolve(in Input, ctx InputContext) Action {
	// Esc/Ctrl-C are Back in every context, including text entry.
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

	// Printable runes. Space and q are honored in both Nav and Command; the
	// directional letters are honored only in Nav (in Command they are commands).
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
