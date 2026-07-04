// Package preview compiles a game's animated preview — the little looping art
// block the arcade shows on the games screen — into the single self-contained
// preview.scp bundle a game ships as a release asset.
//
// You author a preview as a preview.yaml manifest plus plain-text frame files
// (46×7 cells, optional ANSI SGR color); Pack validates and packs them into the
// bundle, and Load turns a bundle back into a playable Animation. This is the
// authoring side of the contract — the arcade re-runs every validation limit
// here when it ingests a bundle, so a preview that Pack accepts is one the
// arcade will accept. Playback loops implicitly from the last frame back to the
// first; there is nothing to configure.
package preview

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Geometry and limits. Cols×Rows is the games-screen art block's interior: the
// arcade draws the border, you get the inside.
const (
	Cols      = 46 // display cells per row (wide glyphs count 2)
	Rows      = 7  // rows per frame
	MaxFrames = 64
	MinFrames = 1
	// MinFrameMS / MaxFrameMS clamp per-frame durations; MaxBundleBytes caps
	// the packed artifact.
	MinFrameMS     = 80
	MaxFrameMS     = 10000
	MaxBundleBytes = 64 * 1024
	// Version is the bundle format version this package writes and accepts. An
	// unknown version loads as "no preview" (fallback), never an error page.
	Version = 1
)

// Frame is one packed frame: exactly Rows rows of exactly Cols display cells
// (already space-padded by the packer), plus its display duration.
type Frame struct {
	MS   int
	Rows []string
}

// Animation is a loaded, validated preview loop. Playback loops from the last
// frame back to the first; looping is implicit and always on.
type Animation struct {
	Frames []Frame
}

// bundleJSON is the preview.scp wire form: {"v":1,"frames":[{"ms":…,"lines":[…]}]}.
type bundleJSON struct {
	V      int         `json:"v"`
	Frames []frameJSON `json:"frames"`
}

type frameJSON struct {
	MS    int      `json:"ms"`
	Lines []string `json:"lines"`
}

// Load parses and validates a preview.scp bundle. Every limit is re-checked
// here regardless of what packed the file; durations outside the clamp are
// clamped (not rejected). Any structural violation returns an error — callers
// treat that as "no preview", never a fatal condition.
func Load(data []byte) (*Animation, error) {
	if len(data) > MaxBundleBytes {
		return nil, fmt.Errorf("bundle is %d bytes; limit %d", len(data), MaxBundleBytes)
	}
	var b bundleJSON
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("bundle parse: %w", err)
	}
	if b.V != Version {
		return nil, fmt.Errorf("bundle version %d; this host speaks %d", b.V, Version)
	}
	if len(b.Frames) < MinFrames || len(b.Frames) > MaxFrames {
		return nil, fmt.Errorf("bundle has %d frames; want %d..%d", len(b.Frames), MinFrames, MaxFrames)
	}
	anim := &Animation{Frames: make([]Frame, len(b.Frames))}
	for i, f := range b.Frames {
		if len(f.Lines) != Rows {
			return nil, fmt.Errorf("frame %d has %d rows; want exactly %d", i, len(f.Lines), Rows)
		}
		rows := make([]string, Rows)
		for r, line := range f.Lines {
			if err := validateText(line); err != nil {
				return nil, fmt.Errorf("frame %d row %d: %w", i, r, err)
			}
			if w := ansi.StringWidth(line); w != Cols {
				return nil, fmt.Errorf("frame %d row %d is %d cells; want exactly %d", i, r, w, Cols)
			}
			rows[r] = line
		}
		anim.Frames[i] = Frame{MS: clampMS(f.MS), Rows: rows}
	}
	return anim, nil
}

// clampMS bounds a per-frame duration to [MinFrameMS, MaxFrameMS].
func clampMS(ms int) int {
	if ms < MinFrameMS {
		return MinFrameMS
	}
	if ms > MaxFrameMS {
		return MaxFrameMS
	}
	return ms
}

// validateText enforces the frame-text contract: printable text plus SGR
// styling only. Every ESC must open a well-formed SGR sequence
// (ESC '[' params 'm', params drawn from digits ';' ':'), and no other
// control characters are allowed — including the C1 range (U+0080–U+009F:
// U+009B is a one-character CSI that would smuggle cursor movement past an
// ESC-only scan) and invalid UTF-8. Frames are pure styled text, never
// cursor movement or terminal state.
//
// Width-treacherous glyph machinery is rejected too: variation selectors,
// ZWJ, and the keycap combiner render at terminal-dependent widths (the
// keycap class corrupted a slot-machine preview in production), so a frame
// that measures 46 cells here could overflow the art block on a real terminal.
func validateText(s string) error {
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j, err := scanSGR(s, i)
			if err != nil {
				return err
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			return fmt.Errorf("invalid UTF-8 at byte %d", i)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return fmt.Errorf("control character %#x; frames are text plus SGR styling only", r)
		case r == 0xfe0f || r == 0x200d || r == 0x20e3:
			return fmt.Errorf("glyph-width hazard %#x: variation selectors, ZWJ, and keycap combiners render at terminal-dependent widths", r)
		}
		i += size
	}
	return nil
}

// scanSGR checks that the escape sequence starting at s[i] is a complete SGR
// and returns the index just past it.
func scanSGR(s string, i int) (int, error) {
	rest := s[i:]
	if len(rest) < 2 || rest[1] != '[' {
		return 0, fmt.Errorf("escape sequence %q: only SGR (ESC[…m) styling is allowed", clipSeq(rest))
	}
	for j := 2; j < len(rest); j++ {
		switch c := rest[j]; {
		case c == 'm':
			return i + j + 1, nil
		case c >= '0' && c <= '9', c == ';', c == ':':
			// param bytes — keep scanning
		default:
			return 0, fmt.Errorf("escape sequence %q: only SGR (ESC[…m) styling is allowed", clipSeq(rest[:j+1]))
		}
	}
	return 0, fmt.Errorf("unterminated escape sequence %q", clipSeq(rest))
}

// clipSeq bounds an offending sequence for error messages.
func clipSeq(s string) string {
	const max = 12
	if len(s) > max {
		s = s[:max]
	}
	return strings.ReplaceAll(s, "\x1b", `\x1b`)
}

// pad returns line space-padded on the right to exactly Cols cells, closing
// any open SGR styling first so padding (and anything after it) is unstyled.
// The caller has already validated width ≤ Cols.
func pad(line string) string {
	w := ansi.StringWidth(line)
	if strings.Contains(line, "\x1b") {
		line += "\x1b[0m"
	}
	return line + strings.Repeat(" ", Cols-w)
}
