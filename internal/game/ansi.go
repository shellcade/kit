package game

import (
	"fmt"
	"strings"
	"time"
)

// ANSIRows renders each frame row as a truecolor ANSI string (SGR emitted on
// style change, grapheme clusters burst unbroken, reset at row end). It is the
// single encoder behind the dev runner's terminal output and the smoke
// runner's shot files — one renderer so both surfaces emit identical bytes.
func ANSIRows(f *Frame) []string {
	rows := make([]string, Rows)
	var b strings.Builder
	for row := 0; row < Rows; row++ {
		b.Reset()
		b.Grow(Cols * 8)
		last := ""
		for col := 0; col < Cols; col++ {
			c := f.Cells[row][col]
			if c.Cont {
				continue
			}
			sgr := cellSGR(c)
			if sgr != last {
				b.WriteString(sgr)
				last = sgr
			}
			ru := c.Rune
			if ru == 0 || ru < 0x20 {
				ru = ' '
			}
			b.WriteRune(ru)
			// Burst the grapheme cluster's extra code points immediately after
			// the base, before the next cell, so the terminal receives the
			// cluster (base VS16 / base + keycap / base ZWJ piece) unbroken.
			if c.Cp2 != 0 {
				b.WriteRune(c.Cp2)
				if c.Cp3 != 0 {
					b.WriteRune(c.Cp3)
				}
			}
		}
		b.WriteString("\x1b[0m")
		rows[row] = b.String()
	}
	return rows
}

// TextRows renders each frame row as plain text: full grapheme clusters, no
// escape sequences, trailing blanks trimmed — the greppable twin of ANSIRows.
func TextRows(f *Frame) []string {
	rows := make([]string, Rows)
	var b strings.Builder
	for row := 0; row < Rows; row++ {
		b.Reset()
		end := Cols
		for end > 0 {
			c := f.Cells[row][end-1]
			if c.Cont || ((c.Rune == 0 || c.Rune == ' ') && c.Cp2 == 0) {
				end--
				continue
			}
			break
		}
		for col := 0; col < end; col++ {
			c := f.Cells[row][col]
			if c.Cont {
				continue
			}
			ru := c.Rune
			if ru == 0 || ru < 0x20 {
				ru = ' '
			}
			b.WriteRune(ru)
			if c.Cp2 != 0 {
				b.WriteRune(c.Cp2)
				if c.Cp3 != 0 {
					b.WriteRune(c.Cp3)
				}
			}
		}
		rows[row] = b.String()
	}
	return rows
}

// frameToANSI joins ANSIRows with CRLF for the raw-mode terminal (dev runner).
func frameToANSI(f *Frame) string {
	return strings.Join(ANSIRows(f), "\r\n")
}

func cellSGR(c Cell) string {
	var parts []string
	parts = append(parts, "0")
	if c.Attr&AttrBold != 0 {
		parts = append(parts, "1")
	}
	if c.Attr&AttrDim != 0 {
		parts = append(parts, "2")
	}
	if c.Attr&AttrUnderline != 0 {
		parts = append(parts, "4")
	}
	if c.Attr&AttrReverse != 0 {
		parts = append(parts, "7")
	}
	if c.FG.set {
		parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", c.FG.r, c.FG.g, c.FG.b))
	}
	if c.BG.set {
		parts = append(parts, fmt.Sprintf("48;2;%d;%d;%d", c.BG.r, c.BG.g, c.BG.b))
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

// SeedEpoch derives a fixed virtual-clock start from a run seed, so the same
// seed always begins at the same instant. The year-2000 base keeps the value
// human-readable in logs while staying well clear of the zero time. Shared by
// the dev runner's -seed mode and the smoke runner.
func SeedEpoch(seed int64) time.Time {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	// Spread by seed but keep it bounded and positive (mod a ~year of seconds).
	off := seed % (365 * 24 * 3600)
	if off < 0 {
		off += 365 * 24 * 3600
	}
	return base.Add(time.Duration(off) * time.Second)
}
