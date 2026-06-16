package canvas

// Color is a truecolor source value. The renderer downgrades it per the
// session's color depth at encode time; the canvas itself only stores the
// authoritative RGB (or "default / unset").
type Color struct {
	set     bool
	r, g, b uint8
}

// Default is the unset color (terminal default fg/bg).
func Default() Color { return Color{} }

// RGB constructs a truecolor value.
func RGB(r, g, b uint8) Color { return Color{set: true, r: r, g: g, b: b} }

// Gray is a convenience for an equal-channel gray.
func Gray(v uint8) Color { return RGB(v, v, v) }

// IsSet reports whether the color is set (vs. terminal default).
func (c Color) IsSet() bool { return c.set }

// RGB returns the color's red/green/blue channels (the renderer downgrades
// these per session color depth).
func (c Color) RGB() (uint8, uint8, uint8) { return c.r, c.g, c.b }

// Equal reports color equality (used by the cell diff).
func (c Color) Equal(o Color) bool {
	if c.set != o.set {
		return false
	}
	if !c.set {
		return true
	}
	return c.r == o.r && c.g == o.g && c.b == o.b
}

// Some shared palette entries used by the lobby and games.
var (
	White   = RGB(0xff, 0xff, 0xff)
	Black   = RGB(0x00, 0x00, 0x00)
	Red     = RGB(0xff, 0x55, 0x55)
	Green   = RGB(0x55, 0xff, 0x55)
	Yellow  = RGB(0xff, 0xff, 0x55)
	Blue    = RGB(0x55, 0x99, 0xff)
	Cyan    = RGB(0x55, 0xff, 0xff)
	Magenta = RGB(0xff, 0x77, 0xff)
	DimGray = Gray(0x6c)
)
