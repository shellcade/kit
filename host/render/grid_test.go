package render

import (
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/session"
)

func TestGridToANSIShape(t *testing.T) {
	g := canvas.New()
	g.Text(0, 0, "hi", canvas.Style{FG: canvas.Cyan})
	out := GridToANSI(g, session.Caps{ColorDepth: session.ColorTrue, UTF8: true})

	lines := strings.Split(out, "\n")
	if len(lines) != canvas.Rows {
		t.Fatalf("expected %d lines, got %d", canvas.Rows, len(lines))
	}
	for i, ln := range lines {
		if !strings.HasSuffix(ln, "\x1b[0m") {
			t.Errorf("line %d does not end with an SGR reset: %q", i, ln)
		}
	}
	if !strings.Contains(lines[0], "hi") {
		t.Errorf("row 0 missing content: %q", lines[0])
	}
}

func TestGridToANSIColorDowngrade(t *testing.T) {
	g := canvas.New()
	g.Text(0, 0, "x", canvas.Style{FG: canvas.RGB(0x12, 0x34, 0x56)})
	// truecolor uses 38;2;r;g;b ; 16-color must not.
	tc := GridToANSI(g, session.Caps{ColorDepth: session.ColorTrue})
	if !strings.Contains(tc, "38;2;") {
		t.Errorf("truecolor output should use 24-bit SGR, got %q", firstLine(tc))
	}
	c16 := GridToANSI(g, session.Caps{ColorDepth: session.Color16})
	if strings.Contains(c16, "38;2;") {
		t.Errorf("16-color output must not use 24-bit SGR, got %q", firstLine(c16))
	}
}

func TestGridToANSIASCIIFallback(t *testing.T) {
	g := canvas.New()
	g.Text(0, 0, "│", canvas.Style{}) // box-drawing vertical
	noUTF := GridToANSI(g, session.Caps{UTF8: false})
	if !strings.Contains(firstLine(noUTF), "|") {
		t.Errorf("expected ASCII fallback '|' without UTF-8, got %q", firstLine(noUTF))
	}
}

// TestGridToANSIWideEmojiASCIIFallback: a width-2 glyph (emoji or fullwidth
// form) degrades to a mnemonic ASCII char, and its continuation cell pads with
// a space so the substitution never changes the row's column count (the
// viewport spec's capability-aware styling requirement).
func TestGridToANSIWideEmojiASCIIFallback(t *testing.T) {
	g := canvas.New()
	wide := func(col int, r rune) {
		g.Set(0, col, canvas.Cell{Rune: r})
		g.Set(0, col+1, canvas.Cell{Cont: true})
	}
	wide(0, '\U0001F352') // 🍒 cherry
	wide(2, '\U0001F514') // 🔔 bell
	wide(4, '⭐')          // ⭐ star
	wide(6, '\U0001F48E') // 💎 gem
	wide(8, '７')          // U+FF17 fullwidth seven

	vis := stripSGR(firstLine(GridToANSI(g, session.Caps{UTF8: false})))
	if want := "C B * D 7 "; !strings.HasPrefix(vis, want) {
		t.Errorf("ASCII row = %q…, want prefix %q", vis[:20], want)
	}
	if n := len([]rune(vis)); n != canvas.Cols {
		t.Errorf("ASCII row is %d visible columns, want %d (degraded wide glyphs must keep the column count)", n, canvas.Cols)
	}
}

// stripSGR removes ANSI SGR sequences, leaving only visible glyphs.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestLetterboxExactSizeReturnsBody(t *testing.T) {
	body := GridToANSI(canvas.New(), session.Caps{})
	if got := Letterbox(body, canvas.Cols, canvas.Rows, session.ColorNone); got != body {
		t.Error("80x24 terminal must return the body unchanged (no letterbox)")
	}
}

func TestLetterboxFillsTerminal(t *testing.T) {
	body := GridToANSI(canvas.New(), session.Caps{})
	out := Letterbox(body, 100, 30, session.ColorNone)
	if n := strings.Count(out, "\n") + 1; n != 30 {
		t.Fatalf("expected 30 rows for a 100x30 terminal, got %d", n)
	}
}

func TestUndersizedMessage(t *testing.T) {
	out := Undersized(70, 20)
	if !strings.Contains(out, "Please resize your terminal to at least 80x24. Current size: 70x20.") {
		t.Errorf("undersized message missing/incorrect: %q", out)
	}
	if n := strings.Count(out, "\n") + 1; n != 20 {
		t.Fatalf("expected 20 rows for a 70x20 terminal, got %d", n)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
