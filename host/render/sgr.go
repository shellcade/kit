package render

import (
	"strconv"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/session"
)

// styleKey is the resolved styling of a cell for diffing.
type styleKey struct {
	fg   canvas.Color
	bg   canvas.Color
	attr canvas.Attr
}

// knownAttr is the set of attribute bits the renderer assigns an SGR code to.
// The host MUST ignore unknown attr bits (ABI v2 evolution rule §5): masking
// here means an undefined bit neither leaks an SGR parameter NOR triggers a
// spurious style change (which would re-emit a redundant full SGR), so a future
// minor can assign a new attribute additively.
const knownAttr = canvas.AttrBold | canvas.AttrDim | canvas.AttrUnderline | canvas.AttrReverse

func keyOf(c canvas.Cell) styleKey { return styleKey{fg: c.FG, bg: c.BG, attr: c.Attr & knownAttr} }

// appendSGR writes a full SGR reset followed by this style's parameters,
// downgraded to the session's color depth.
func appendSGR(buf []byte, k styleKey, depth session.ColorDepth) []byte {
	buf = append(buf, "\x1b[0"...) // reset, then add params
	if k.attr&canvas.AttrBold != 0 {
		buf = append(buf, ";1"...)
	}
	if k.attr&canvas.AttrDim != 0 {
		buf = append(buf, ";2"...)
	}
	if k.attr&canvas.AttrUnderline != 0 {
		buf = append(buf, ";4"...)
	}
	if k.attr&canvas.AttrReverse != 0 {
		buf = append(buf, ";7"...)
	}
	if depth != session.ColorNone {
		buf = appendColor(buf, k.fg, depth, true)
		buf = appendColor(buf, k.bg, depth, false)
	}
	buf = append(buf, 'm')
	return buf
}

func appendColor(buf []byte, c canvas.Color, depth session.ColorDepth, fg bool) []byte {
	if !c.IsSet() {
		return buf
	}
	r, g, b := colorRGB(c)
	switch depth {
	case session.ColorTrue:
		base := 48
		if fg {
			base = 38
		}
		buf = append(buf, ';')
		buf = strconv.AppendInt(buf, int64(base), 10)
		buf = append(buf, ";2;"...)
		buf = strconv.AppendInt(buf, int64(r), 10)
		buf = append(buf, ';')
		buf = strconv.AppendInt(buf, int64(g), 10)
		buf = append(buf, ';')
		buf = strconv.AppendInt(buf, int64(b), 10)
	case session.Color256:
		base := 48
		if fg {
			base = 38
		}
		buf = append(buf, ';')
		buf = strconv.AppendInt(buf, int64(base), 10)
		buf = append(buf, ";5;"...)
		buf = strconv.AppendInt(buf, int64(rgbTo256(r, g, b)), 10)
	case session.Color16:
		code := rgbTo16(r, g, b, fg)
		buf = append(buf, ';')
		buf = strconv.AppendInt(buf, int64(code), 10)
	}
	return buf
}

func colorRGB(c canvas.Color) (uint8, uint8, uint8) { return c.RGB() }

func rgbTo256(r, g, b uint8) int {
	if r == g && g == b {
		// grayscale ramp 232..255 (24 steps), plus endpoints
		if r < 8 {
			return 16
		}
		if r > 248 {
			return 231
		}
		return 232 + int((int(r)-8)*24/247)
	}
	q := func(v uint8) int { return int(v) * 5 / 255 }
	return 16 + 36*q(r) + 6*q(g) + q(b)
}

func rgbTo16(r, g, b uint8, fg bool) int {
	bright := 0
	if int(r)+int(g)+int(b) > 384 {
		bright = 60
	}
	code := 0
	if r > 96 {
		code |= 1
	}
	if g > 96 {
		code |= 2
	}
	if b > 96 {
		code |= 4
	}
	base := 30
	if !fg {
		base = 40
	}
	return base + code + bright
}
