// Package canvas owns the fixed 80x24 cell grid that the lobby and every game
// render into. The Cell type and the Grid type are defined here and aliased by
// the game SDK; a single shared definition governs every composed frame.
package canvas

// The fixed drawable canvas. This is a system-wide invariant in v1.
const (
	Cols = 80
	Rows = 24
)

// Attr is a bitset of text attributes applied to a cell.
type Attr uint8

const (
	AttrBold Attr = 1 << iota
	AttrDim
	AttrUnderline
	AttrReverse
)

// Cell is a single drawable position: a rune plus foreground/background color
// and attributes. A double-width rune occupies two columns; the trailing column
// is a continuation cell (Cont == true, Rune == 0).
//
// In ABI v2 a cell may carry up to three code points of a grapheme cluster:
// Rune is the base, Cp2/Cp3 the extra code points (0 = unused; e.g. a VS16
// selector, a skin-tone modifier, a ZWJ piece, or a keycap U+20E3). Renderers
// emit base+Cp2+Cp3 as one contiguous UTF-8 burst. Single-code-point cells
// leave Cp2/Cp3 zero by zero-value, so existing content is unchanged.
type Cell struct {
	Rune rune
	Cp2  rune // second grapheme code point (0 = unused)
	Cp3  rune // third grapheme code point (0 = unused)
	FG   Color
	BG   Color
	Attr Attr
	Cont bool // continuation column of a wide rune to the left
}

// Style bundles the styling applied when writing text into the grid.
type Style struct {
	FG   Color
	BG   Color
	Attr Attr
}

// Grid is the fixed Rows x Cols cell grid. It is intrinsically 80x24, so a game
// can never accidentally exceed the canvas. It is passed by value as a Frame.
type Grid struct {
	Cells [Rows][Cols]Cell
}

// blank is a space with default colors.
func blank() Cell { return Cell{Rune: ' '} }

// New returns a grid filled with blank cells.
func New() Grid {
	var g Grid
	for r := 0; r < Rows; r++ {
		for c := 0; c < Cols; c++ {
			g.Cells[r][c] = blank()
		}
	}
	return g
}

// inBounds reports whether (row, col) is on the canvas.
func inBounds(row, col int) bool {
	return row >= 0 && row < Rows && col >= 0 && col < Cols
}

// Set writes a single cell, clamping (silently dropping) out-of-bounds writes.
func (g *Grid) Set(row, col int, cell Cell) {
	if !inBounds(row, col) {
		return
	}
	g.Cells[row][col] = cell
}

// SetRune writes one rune with a style. Out-of-bounds is dropped. Width is
// treated as 1 (v1 corpus is ASCII); callers wanting wide-rune handling should
// pre-account for the continuation column.
func (g *Grid) SetRune(row, col int, r rune, st Style) {
	g.Set(row, col, Cell{Rune: r, FG: st.FG, BG: st.BG, Attr: st.Attr})
}

// Text blits a string starting at (row, col), left to right, clamping any
// portion that would exceed the grid. Returns the column just past the written
// text (may be off-canvas). Tabs/newlines are not interpreted.
func (g *Grid) Text(row, col int, s string, st Style) int {
	c := col
	for _, r := range s {
		if r == '\n' || r == '\t' {
			r = ' '
		}
		g.SetRune(row, c, r, st)
		c++
	}
	return c
}

// TextRight blits a string so that it ends at column end-1 (right-aligned),
// clamping on the left if needed.
func (g *Grid) TextRight(row, end int, s string, st Style) {
	g.Text(row, end-len([]rune(s)), s, st)
}

// Fill sets every cell in the inclusive rectangle to cell (clamped).
func (g *Grid) Fill(r0, c0, r1, c1 int, cell Cell) {
	for r := r0; r <= r1; r++ {
		for c := c0; c <= c1; c++ {
			g.Set(r, c, cell)
		}
	}
}

// ClearRow blanks an entire row.
func (g *Grid) ClearRow(row int) {
	for c := 0; c < Cols; c++ {
		g.Set(row, c, blank())
	}
}
