package canvas

import (
	"strings"
	"testing"
)

func TestGridToASCIIIsColorIndependent(t *testing.T) {
	a := New()
	b := New()
	a.Text(3, 5, "hello", Style{FG: Red, Attr: AttrBold})
	b.Text(3, 5, "hello", Style{FG: Green}) // different color/attr, same runes
	if GridToASCII(a) != GridToASCII(b) {
		t.Fatal("GridToASCII must ignore color/attr differences")
	}
}

func TestGridToASCIIShapeAndTrim(t *testing.T) {
	g := New()
	g.Text(0, 0, "top", styleNone())
	g.Text(Rows-1, 0, "bottom", styleNone())
	out := GridToASCII(g)

	rows := strings.Split(out, "\n")
	if len(rows) != Rows {
		t.Fatalf("expected %d rows, got %d", Rows, len(rows))
	}
	if rows[0] != "top" {
		t.Errorf("row 0: trailing spaces not trimmed: %q", rows[0])
	}
	if rows[Rows-1] != "bottom" {
		t.Errorf("last row: got %q", rows[Rows-1])
	}
	if strings.HasSuffix(out, "\n") {
		t.Error("output must not end with a trailing newline")
	}
	// A blank grid is 23 newlines and nothing else.
	if got := GridToASCII(New()); got != strings.Repeat("\n", Rows-1) {
		t.Errorf("blank grid: got %q", got)
	}
}

func styleNone() Style { return Style{} }
