//go:build !wasip1 && !tinygo.wasm

package game

import "time"

// Input parsing for the native dev runner. Terminals deliver keystrokes as raw
// byte streams, and three realities make naive parsing wrong:
//
//   - Escape sequences ("\x1b[A" for ↑) can be SPLIT across reads — the ESC in
//     one Read, the "[A" in the next.
//   - A lone ESC (the user pressed Escape to leave) is the PREFIX of every
//     arrow key, so it is only knowable as "bare" once nothing follows it
//     within a short window.
//   - Paste pastes arrive as one big burst: many bytes in a single Read.
//
// keyParser is a small stateful machine that consumes bytes incrementally and
// emits events, holding a partial escape sequence (or an ambiguous bare ESC)
// across reads. The runtime (devrun.go) feeds it read buffers and a timeout
// tick; this type has no I/O so it is unit-testable.

// escTimeout is how long a lone ESC waits for a continuation before it is
// resolved as a bare Escape press. Long enough to absorb a split-read arrow on
// any sane link, short enough that pressing Escape to leave feels instant.
const escTimeout = 40 * time.Millisecond

// eventKind tags a parsed event.
type eventKind uint8

const (
	evInput      eventKind = iota // a normal Input (in the Input field)
	evLeave                       // Ctrl-C or a resolved bare ESC: leave
	evSeatSwitch                  // Ctrl-T: next hot-seat
)

// parsedEvent is one decoded terminal event.
type parsedEvent struct {
	Kind  eventKind
	Input Input
}

// parseState is the parser's escape-sequence state.
type parseState uint8

const (
	stGround parseState = iota // not mid-sequence
	stEsc                      // saw ESC, waiting to learn if it's CSI or bare
	stCSI                      // saw ESC '[', collecting the final byte
)

// keyParser decodes a raw terminal byte stream into parsedEvents. The zero
// value is ready to use (ground state).
type keyParser struct {
	state parseState
}

// Feed consumes the bytes from one read and appends decoded events to out
// (returned). A held escape (state != stGround) carries to the next Feed, so a
// sequence split across reads is parsed correctly. The caller flushes a stuck
// bare ESC via Timeout when no more bytes arrive promptly.
func (p *keyParser) Feed(buf []byte, out []parsedEvent) []parsedEvent {
	for _, b := range buf {
		switch p.state {
		case stEsc:
			if b == '[' {
				p.state = stCSI
				continue
			}
			// ESC immediately followed by another byte: the ESC was a bare
			// Escape (leave). We surface the leave and re-process this byte
			// from ground — but a leave ends the session, so stop here.
			p.state = stGround
			out = append(out, parsedEvent{Kind: evLeave})
			return out
		case stCSI:
			out = appendCSI(out, b)
			p.state = stGround
			continue
		}
		// Ground state.
		switch {
		case b == 0x03: // Ctrl-C
			return append(out, parsedEvent{Kind: evLeave})
		case b == 0x14: // Ctrl-T: next seat
			out = append(out, parsedEvent{Kind: evSeatSwitch})
		case b == 0x1b: // ESC: ambiguous until the next byte (or a timeout)
			p.state = stEsc
		case b == '\r' || b == '\n':
			out = append(out, keyEvent(KeyEnter))
		case b == 0x7f || b == 0x08: // DEL / Backspace
			out = append(out, keyEvent(KeyBackspace))
		case b == '\t':
			out = append(out, keyEvent(KeyTab))
		case b >= 0x20:
			out = append(out, parsedEvent{Kind: evInput, Input: Input{Kind: InputRune, Rune: rune(b)}})
		}
	}
	return out
}

// Timeout resolves a held bare ESC into a leave event. It is called when no
// bytes have arrived for escTimeout while the parser sits in stEsc. A held CSI
// (state stCSI) is a genuinely truncated sequence and is simply discarded —
// the next byte starts fresh.
func (p *keyParser) Timeout(out []parsedEvent) []parsedEvent {
	switch p.state {
	case stEsc:
		p.state = stGround
		return append(out, parsedEvent{Kind: evLeave})
	case stCSI:
		p.state = stGround
	}
	return out
}

// Pending reports whether the parser is mid-sequence (so the runtime should arm
// the bare-ESC timeout).
func (p *keyParser) Pending() bool { return p.state != stGround }

// appendCSI maps the final byte of a CSI sequence (ESC '[' X) to an event,
// dropping unrecognised sequences (e.g. mouse, F-keys) rather than erroring.
func appendCSI(out []parsedEvent, final byte) []parsedEvent {
	switch final {
	case 'A':
		return append(out, keyEvent(KeyUp))
	case 'B':
		return append(out, keyEvent(KeyDown))
	case 'C':
		return append(out, keyEvent(KeyRight))
	case 'D':
		return append(out, keyEvent(KeyLeft))
	}
	return out
}

func keyEvent(k Key) parsedEvent {
	return parsedEvent{Kind: evInput, Input: Input{Kind: InputKey, Key: k}}
}
