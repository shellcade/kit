package render

import (
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/session"
)

func TestAsciiFallbackForBoxDrawingWhenNoUTF8(t *testing.T) {
	cases := map[rune]string{
		'─': "-", '│': "|",
		'╭': "+", '╮': "+", '╰': "+", '╯': "+",
		'┌': "+", '┐': "+", '└': "+", '┘': "+",
		'●': "o", '○': "o",
	}
	for in, want := range cases {
		got := string(appendGlyphCaps(nil, canvas.Cell{Rune: in}, false))
		if got != want {
			t.Errorf("appendGlyphCaps(%q) without UTF8 = %q, want %q", in, got, want)
		}
	}
}

func TestAsciiFallbackForCardSuitsWhenNoUTF8(t *testing.T) {
	cases := map[rune]string{'♠': "S", '♥': "H", '♦': "D", '♣': "C"}
	for in, want := range cases {
		got := string(appendGlyphCaps(nil, canvas.Cell{Rune: in}, false))
		if got != want {
			t.Errorf("appendGlyphCaps(%q) without UTF8 = %q, want %q", in, got, want)
		}
	}
}

func TestAsciiFallbackForChessFigurinesWhenNoUTF8(t *testing.T) {
	cases := map[rune]string{
		'♔': "K", '♕': "Q", '♖': "R", '♗': "B", '♘': "N", '♙': "P",
		'♚': "k", '♛': "q", '♜': "r", '♝': "b", '♞': "n", '♟': "p",
	}
	for in, want := range cases {
		got := string(appendGlyphCaps(nil, canvas.Cell{Rune: in}, false))
		if got != want {
			t.Errorf("appendGlyphCaps(%q) without UTF8 = %q, want %q", in, got, want)
		}
	}
}

func TestFigurinesAndBoxDrawingPassThroughWithUTF8(t *testing.T) {
	for _, r := range []rune{'─', '♔', '♟', '♛'} {
		if got := string(appendGlyphCaps(nil, canvas.Cell{Rune: r}, true)); got != string(r) {
			t.Errorf("with UTF8, %q = %q, want it passed through", r, got)
		}
	}
}

func TestUnmappedNonAsciiStillBecomesQuestionMark(t *testing.T) {
	// 🍒 graduated to a mapped slot face; a unicorn stays unmapped.
	if got := string(appendGlyphCaps(nil, canvas.Cell{Rune: '🦄'}, false)); got != "?" {
		t.Errorf("unmapped non-ascii without UTF8 = %q, want ?", got)
	}
}

func TestContinuationCellEmitsNothing(t *testing.T) {
	if got := appendGlyphCaps(nil, canvas.Cell{Cont: true}, true); len(got) != 0 {
		t.Errorf("continuation cell emitted %q, want nothing", string(got))
	}
}

// TestGraphemeBurstContiguousWithUTF8: a cell carrying base+Cp2+Cp3 emits all
// three code points as one contiguous UTF-8 burst (a VS16 emoji, a skin-tone
// modifier, a keycap base+U+20E3), with nothing interleaved (D3b).
func TestGraphemeBurstContiguousWithUTF8(t *testing.T) {
	cases := []struct {
		name      string
		cell      canvas.Cell
		wantRunes []rune
	}{
		{"vs16", canvas.Cell{Rune: '☂', Cp2: 0xFE0F}, []rune{'☂', 0xFE0F}},
		{"skin-tone", canvas.Cell{Rune: '👍', Cp2: 0x1F3FD}, []rune{'👍', 0x1F3FD}},
		{"keycap", canvas.Cell{Rune: '1', Cp2: 0xFE0F, Cp3: 0x20E3}, []rune{'1', 0xFE0F, 0x20E3}},
	}
	for _, tc := range cases {
		got := string(appendGlyphCaps(nil, tc.cell, true))
		if want := string(tc.wantRunes); got != want {
			t.Errorf("%s: burst = %q (% x), want %q (% x)", tc.name, got, got, want, want)
		}
	}
}

// TestGraphemeBurstDroppedWithoutUTF8: with UTF-8 off the base degrades to ASCII
// and Cp2/Cp3 are dropped (a legal single-glyph fallback).
func TestGraphemeBurstDroppedWithoutUTF8(t *testing.T) {
	// base '1' is ASCII so it survives; the VS16 + keycap are dropped.
	got := string(appendGlyphCaps(nil, canvas.Cell{Rune: '1', Cp2: 0xFE0F, Cp3: 0x20E3}, false))
	if got != "1" {
		t.Errorf("UTF-8-off keycap = %q, want \"1\" (cps dropped)", got)
	}
}

// TestUnknownAttrBitsNeitherErrorNorLeakSGR: a cell whose attr byte sets a bit
// the renderer does not assign (only Bold/Dim/Underline/Reverse are assigned)
// renders identically to the same cell with that bit cleared — the unknown bit
// leaks no SGR parameter and does not error (evolution rule §5 / task 3.4).
func TestUnknownAttrBitsNeitherErrorNorLeakSGR(t *testing.T) {
	caps := session.Caps{ColorDepth: session.ColorTrue, UTF8: true}
	known := canvas.AttrBold | canvas.AttrDim | canvas.AttrUnderline | canvas.AttrReverse
	for bit := 0; bit < 8; bit++ {
		attr := canvas.Attr(1 << bit)
		if attr&known != 0 {
			continue // skip assigned bits
		}
		g := canvas.New()
		g.Set(0, 0, canvas.Cell{Rune: 'X', Attr: attr})
		withUnknown := GridToANSI(g, caps)

		g2 := canvas.New()
		g2.Set(0, 0, canvas.Cell{Rune: 'X'})
		plain := GridToANSI(g2, caps)

		if withUnknown != plain {
			t.Errorf("attr bit %d leaked into the SGR stream:\n  with=%q\n plain=%q", bit, withUnknown, plain)
		}
		// And the bit must not surface as a raw SGR parameter anywhere.
		if strings.Contains(withUnknown, ";"+itoa(1<<bit)+"m") {
			t.Errorf("attr bit %d leaked a raw SGR code", bit)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
