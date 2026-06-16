package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/session"
)

// GridToANSI renders g as 24 rows of SGR-styled glyphs separated by "\n", with NO
// cursor positioning. Each row emits a full SGR for its first cell, re-emits SGR
// only when the style changes, and ends with an SGR reset so rows compose cleanly
// into a larger frame. Colors are downgraded to caps.ColorDepth; non-ASCII runes
// degrade to ASCII when caps.UTF8 is false. Every row is exactly canvas.Cols
// visible columns wide (one glyph per cell; continuation cells emit nothing).
//
// This is the full-frame, diff-free encoder used by the bubbletea front door:
// the model converts the active grid to this string for View(), and bubbletea's
// renderer owns the wire diffing.
func GridToANSI(g canvas.Grid, caps session.Caps) string {
	var b strings.Builder
	for row := 0; row < canvas.Rows; row++ {
		var cur styleKey
		haveCur := false
		var line []byte
		for col := 0; col < canvas.Cols; col++ {
			c := g.Cells[row][col]
			k := keyOf(c)
			if !haveCur || k != cur {
				line = appendSGR(line, k, caps.ColorDepth)
				cur, haveCur = k, true
			}
			line = appendGlyphCaps(line, c, caps.UTF8)
		}
		line = append(line, "\x1b[0m"...)
		b.Write(line)
		if row < canvas.Rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// appendGlyphCaps encodes a cell's glyph (mirroring the Renderer's appendGlyph):
// rune 0 / control bytes become spaces, and non-ASCII degrades via
// asciiFallback when UTF-8 is unavailable.
//
// In ABI v2 a cell may carry up to three grapheme code points (base + Cp2 +
// Cp3). When UTF-8 is on, all present code points are emitted as one contiguous
// burst BEFORE the next cell's glyph, so a VS16/skin-tone/ZWJ/keycap sequence
// reaches the terminal adjacent and unbroken. With UTF-8 off the base degrades
// to ASCII and Cp2/Cp3 are dropped (a legal single-glyph fallback).
//
// Continuation cells emit nothing when UTF-8 is on (the wide glyph to their
// left renders both columns) but pad a space when it is off (that glyph
// degraded to ONE ASCII char) — a substitution must never change the row's
// column count.
func appendGlyphCaps(buf []byte, c canvas.Cell, utf8On bool) []byte {
	if c.Cont {
		if !utf8On {
			return append(buf, ' ')
		}
		return buf
	}
	ru := c.Rune
	if ru == 0 || ru < 0x20 {
		ru = ' '
	}
	if ru > 0x7e && !utf8On {
		ru = asciiFallback(ru)
	}
	var tmp [4]byte
	n := utf8.EncodeRune(tmp[:], ru)
	buf = append(buf, tmp[:n]...)
	if !utf8On {
		return buf // ASCII fallback: base only, drop the cluster's extra code points
	}
	if c.Cp2 != 0 {
		n = utf8.EncodeRune(tmp[:], c.Cp2)
		buf = append(buf, tmp[:n]...)
	}
	if c.Cp3 != 0 {
		n = utf8.EncodeRune(tmp[:], c.Cp3)
		buf = append(buf, tmp[:n]...)
	}
	return buf
}

// Letterbox centers an 80×24 body (as produced by GridToANSI — exactly
// canvas.Cols visible columns per row) within a termCols×termRows terminal,
// drawing a dim border in the surrounding margin. When the terminal is not larger
// than the canvas in either dimension, the body is returned unchanged (the
// undersized case is handled separately by Undersized). The result is termRows
// newline-separated lines so it can be the whole View() output.
func Letterbox(body string, termCols, termRows int, depth session.ColorDepth) string {
	if termCols <= canvas.Cols && termRows <= canvas.Rows {
		return body
	}
	offX := (termCols - canvas.Cols) / 2
	if offX < 0 {
		offX = 0
	}
	offY := (termRows - canvas.Rows) / 2
	if offY < 0 {
		offY = 0
	}

	rows := strings.Split(body, "\n")
	border := string(appendSGR(nil, styleKey{fg: canvas.DimGray, attr: canvas.AttrDim}, depth))
	const reset = "\x1b[0m"

	sideW := 0
	if offX > 0 {
		sideW = 2
	}
	horiz := func() string {
		var b strings.Builder
		if offX > 1 {
			b.WriteString(strings.Repeat(" ", offX-1))
		}
		b.WriteString(border)
		b.WriteString(strings.Repeat("-", canvas.Cols+sideW))
		b.WriteString(reset)
		return b.String()
	}

	out := make([]string, termRows)
	blank := strings.Repeat(" ", termCols)
	for r := 0; r < termRows; r++ {
		switch {
		case offY > 0 && (r == offY-1 || r == offY+canvas.Rows):
			out[r] = horiz()
		case r >= offY && r < offY+canvas.Rows:
			i := r - offY
			line := ""
			if i < len(rows) {
				line = rows[i]
			}
			var b strings.Builder
			if offX > 0 {
				if offX > 1 {
					b.WriteString(strings.Repeat(" ", offX-1))
				}
				b.WriteString(border)
				b.WriteString("|")
				b.WriteString(reset)
			}
			b.WriteString(line)
			if offX > 0 {
				b.WriteString(border)
				b.WriteString("|")
				b.WriteString(reset)
			}
			out[r] = b.String()
		default:
			out[r] = blank
		}
	}
	return strings.Join(out, "\n")
}

// Undersized renders the centered "please resize" interstitial that replaces the
// active screen when the terminal is smaller than 80×24, matching the wording the
// retired renderer used. The result is termRows newline-separated lines.
func Undersized(termCols, termRows int) string {
	msg := fmt.Sprintf("Please resize your terminal to at least 80x24. Current size: %dx%d.", termCols, termRows)
	if termRows < 1 {
		termRows = 1
	}
	if termCols < 1 {
		termCols = 1
	}
	row := termRows / 2
	col := (termCols - len(msg)) / 2
	if col < 0 {
		col = 0
	}
	out := make([]string, termRows)
	blank := strings.Repeat(" ", termCols)
	for r := 0; r < termRows; r++ {
		if r == row {
			out[r] = strings.Repeat(" ", col) + msg
		} else {
			out[r] = blank
		}
	}
	return strings.Join(out, "\n")
}
