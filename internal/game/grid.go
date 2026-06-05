package game

// The fixed 80x24 cell grid, mirroring the platform's canvas package: writes
// outside the grid are clamped (never errored), and the packed frame encoding
// preserves the canvas contract host-side.

const (
	Rows = 24
	Cols = 80
)

// Color is an optional truecolor value; the zero value is the terminal default.
type Color struct {
	set     bool
	r, g, b uint8
}

// RGB constructs a truecolor value.
func RGB(r, g, b uint8) Color { return Color{set: true, r: r, g: g, b: b} }

// Gray is a convenience for an even gray.
func Gray(v uint8) Color { return RGB(v, v, v) }

// IsSet reports whether the color is set (vs terminal default).
func (c Color) IsSet() bool { return c.set }

// RGBVals returns the color components.
func (c Color) RGBVals() (uint8, uint8, uint8) { return c.r, c.g, c.b }

// Standard palette (matches the platform canvas constants).
var (
	White   = RGB(0xff, 0xff, 0xff)
	Red     = RGB(0xff, 0x55, 0x55)
	Green   = RGB(0x55, 0xff, 0x55)
	Yellow  = RGB(0xff, 0xff, 0x55)
	Cyan    = RGB(0x55, 0xff, 0xff)
	DimGray = Gray(0x6c)
)

// Attr is a bitset of text attributes.
type Attr uint8

const (
	AttrBold Attr = 1 << iota
	AttrDim
	AttrUnderline
	AttrReverse
)

// Style bundles the styling applied when writing text.
type Style struct {
	FG   Color
	BG   Color
	Attr Attr
}

// Cell is a single drawable position.
type Cell struct {
	Rune rune
	FG   Color
	BG   Color
	Attr Attr
	Cont bool
}

// Frame is the fixed 24x80 grid a game composes and sends.
type Frame struct {
	Cells [Rows][Cols]Cell
}

// NewFrame returns a grid filled with blank cells. Frames are handled by
// POINTER throughout the SDK: a Frame is ~46KB and pass-by-value explodes
// into thousands of wasm locals (pathological compile time and artifact size).
func NewFrame() *Frame {
	f := &Frame{}
	for r := 0; r < Rows; r++ {
		for c := 0; c < Cols; c++ {
			f.Cells[r][c] = Cell{Rune: ' '}
		}
	}
	return f
}

// Clear resets every cell to a blank (space, default colors), so one Frame
// can be reused across renders — the allocation-free steady state the SDK
// recommends (a fresh NewFrame per render is ~46KB of churn).
func (f *Frame) Clear() {
	blank := Cell{Rune: ' '}
	for r := 0; r < Rows; r++ {
		for c := 0; c < Cols; c++ {
			f.Cells[r][c] = blank
		}
	}
}

func inBounds(row, col int) bool { return row >= 0 && row < Rows && col >= 0 && col < Cols }

// Set writes one cell; out-of-bounds writes are clamped (dropped).
func (f *Frame) Set(row, col int, cell Cell) {
	if !inBounds(row, col) {
		return
	}
	f.Cells[row][col] = cell
}

// SetRune writes one styled rune.
func (f *Frame) SetRune(row, col int, r rune, st Style) {
	f.Set(row, col, Cell{Rune: r, FG: st.FG, BG: st.BG, Attr: st.Attr})
}

// SetWide writes a double-width rune: the glyph occupies (row, col) and its
// continuation cell (row, col+1), which is marked Cont=true so the renderer
// skips it (the wide glyph already covers both columns). CJK, many emoji, and
// box-drawing pairs need this.
//
// Edge handling follows Set's drop-on-overflow philosophy: a wide glyph has no
// room when col is out of bounds OR the continuation cell would fall off the
// right edge (col == Cols-1). In that case the whole write is REFUSED (nothing
// is drawn) — a half-glyph would desync every column to its right. Returns the
// next free column (col+2), or col unchanged when the write was refused.
func (f *Frame) SetWide(row, col int, r rune, st Style) int {
	if !inBounds(row, col) || col+1 >= Cols {
		return col
	}
	f.Cells[row][col] = Cell{Rune: r, FG: st.FG, BG: st.BG, Attr: st.Attr}
	f.Cells[row][col+1] = Cell{FG: st.FG, BG: st.BG, Attr: st.Attr, Cont: true}
	return col + 2
}

// Text writes a string left-to-right, clamped to the row. Returns the next col.
func (f *Frame) Text(row, col int, s string, st Style) int {
	for _, r := range s {
		f.SetRune(row, col, r, st)
		col++
	}
	return col
}

// TextRight writes a string so it ends at col `end` (inclusive).
func (f *Frame) TextRight(row, end int, s string, st Style) {
	f.Text(row, end-len([]rune(s))+1, s, st)
}

// Fill paints a rectangle (inclusive bounds) with the given cell.
func (f *Frame) Fill(r0, c0, r1, c1 int, cell Cell) {
	for r := r0; r <= r1; r++ {
		for c := c0; c <= c1; c++ {
			f.Set(r, c, cell)
		}
	}
}
