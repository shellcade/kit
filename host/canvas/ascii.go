package canvas

import "strings"

// GridToASCII renders g as a deterministic, color-independent text block: every
// cell's rune (rune 0 → space), trailing spaces trimmed per row, and the 24 rows
// joined with "\n" (no trailing newline). It is stable across runs for identical
// grid content — color and attribute differences do not affect the output — so it
// is suitable for before/after snapshot artifacts and golden tests.
func GridToASCII(g Grid) string {
	var sb strings.Builder
	for r := 0; r < Rows; r++ {
		var row strings.Builder
		for c := 0; c < Cols; c++ {
			ch := g.Cells[r][c].Rune
			if ch == 0 {
				ch = ' '
			}
			row.WriteRune(ch)
		}
		sb.WriteString(strings.TrimRight(row.String(), " "))
		if r < Rows-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
