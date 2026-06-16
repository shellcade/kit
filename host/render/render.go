// Package render converts the fixed 80x24 canvas.Grid into the styled, ANSI-
// encoded strings the bubbletea front door renders. bubbletea's renderer owns the
// wire — the per-frame diffing and writing — while this package provides the
// full-frame Grid→ANSI encoder (GridToANSI), the letterbox and undersized-resize
// interstitial composers (Letterbox / Undersized), the per-session SGR color
// downgrade (sgr.go), and the non-UTF8 ASCII glyph fallback (asciiFallback).
//
// The former custom cell-diff "renderer of record" was retired when the front
// door moved to bubbletea; see the bubbletea-frontdoor OpenSpec change.
package render

// asciiFallback degrades a non-ASCII rune to a plain-ASCII stand-in for
// sessions without a UTF-8 locale, so box-drawing chassis art reads as +-|
// instead of a wall of '?'. Anything unmapped stays '?'.
func asciiFallback(r rune) rune {
	// Fullwidth ASCII variants (U+FF01..U+FF5E) fold to their ASCII originals
	// — e.g. the pokies fullwidth seven '７' reads as '7'.
	if r >= 0xFF01 && r <= 0xFF5E {
		return r - 0xFEE0
	}
	switch r {
	case '─', '═':
		return '-'
	case '│', '║':
		return '|'
	case '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘',
		'├', '┤', '┬', '┴', '┼',
		'╔', '╗', '╚', '╝', '╠', '╣', '╬':
		return '+'
	case '●', '○', '◌':
		return 'o'
	case '►', '▶', '◄', '◀':
		return '>'
	case '♠':
		return 'S'
	case '♥':
		return 'H'
	case '♦':
		return 'D'
	case '♣':
		return 'C'
	// Chess figurines degrade to their piece letter; the white (outline) set folds
	// to upper-case and the black (filled) set to lower-case, preserving the side.
	case '♔':
		return 'K'
	case '♕':
		return 'Q'
	case '♖':
		return 'R'
	case '♗':
		return 'B'
	case '♘':
		return 'N'
	case '♙':
		return 'P'
	case '♚':
		return 'k'
	case '♛':
		return 'q'
	case '♜':
		return 'r'
	case '♝':
		return 'b'
	case '♞':
		return 'n'
	case '♟':
		return 'p'
	// Slot-machine faces degrade to a mnemonic letter, mirroring the suit map
	// (pokies' reel art; the symbol's config ID where one exists).
	case '\U0001F352': // 🍒 cherry
		return 'C'
	case '\U0001F514': // 🔔 bell
		return 'B'
	case '⭐': // ⭐ star
		return '*'
	case '\U0001F48E': // 💎 gem
		return 'D'
	}
	return '?'
}
