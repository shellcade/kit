package game

import "testing"

func TestSetWideWritesLeadAndContinuation(t *testing.T) {
	f := NewFrame()
	st := Style{FG: Red, Attr: AttrBold}
	next := f.SetWide(5, 10, '世', st)
	if next != 12 {
		t.Fatalf("next col = %d, want 12", next)
	}
	lead := f.Cells[5][10]
	if lead.Rune != '世' || !lead.FG.IsSet() || lead.Attr != AttrBold {
		t.Fatalf("lead cell wrong: %+v", lead)
	}
	if lead.Cont {
		t.Fatal("lead cell must not be a continuation")
	}
	cont := f.Cells[5][11]
	if !cont.Cont {
		t.Fatal("second cell must be a continuation")
	}
	if cont.Rune != 0 {
		t.Fatalf("continuation cell should carry no rune, got %q", cont.Rune)
	}
	// Style carries onto the continuation so the background spans both columns.
	if cont.FG != lead.FG || cont.Attr != lead.Attr {
		t.Fatalf("continuation style mismatch: %+v vs %+v", cont, lead)
	}
}

func TestSetWideRefusesAtRightEdge(t *testing.T) {
	f := NewFrame()
	// col == Cols-1: the continuation would fall off the row — refuse entirely.
	before := f.Cells[0][Cols-1]
	next := f.SetWide(0, Cols-1, '世', Style{})
	if next != Cols-1 {
		t.Fatalf("refused write should return col unchanged, got %d", next)
	}
	if f.Cells[0][Cols-1] != before {
		t.Fatal("right-edge wide write must leave the last cell untouched")
	}
}

func TestSetWideOutOfBoundsDropped(t *testing.T) {
	f := NewFrame()
	// Each of these is fully out of bounds; none should panic or write.
	cases := [][2]int{{-1, 0}, {0, -1}, {Rows, 0}, {0, Cols}, {0, Cols + 5}}
	for _, c := range cases {
		if next := f.SetWide(c[0], c[1], 'X', Style{}); next != c[1] {
			t.Fatalf("OOB SetWide(%d,%d) returned %d, want %d", c[0], c[1], next, c[1])
		}
	}
	// The grid is still entirely blank.
	for r := 0; r < Rows; r++ {
		for col := 0; col < Cols; col++ {
			if cell := f.Cells[r][col]; cell.Rune != ' ' || cell.Cont {
				t.Fatalf("OOB write leaked into (%d,%d): %+v", r, col, cell)
			}
		}
	}
}

func TestSetWideLastValidColumn(t *testing.T) {
	f := NewFrame()
	// col == Cols-2 is the last column a wide glyph fits in: lead at Cols-2,
	// continuation at Cols-1.
	next := f.SetWide(0, Cols-2, '城', Style{})
	if next != Cols {
		t.Fatalf("next col = %d, want %d", next, Cols)
	}
	if f.Cells[0][Cols-2].Rune != '城' {
		t.Fatal("lead glyph not written at last fitting column")
	}
	if !f.Cells[0][Cols-1].Cont {
		t.Fatal("continuation not written at the last column")
	}
}

func TestTextStaysSingleWidth(t *testing.T) {
	// Frame.Text remains 1-rune-1-col: it writes a wide rune into a single cell
	// and never sets Cont. (SetWide is the opt-in for double-width.)
	f := NewFrame()
	next := f.Text(0, 0, "世界", Style{})
	if next != 2 {
		t.Fatalf("Text advanced %d cols for 2 runes, want 2", next)
	}
	if f.Cells[0][0].Cont || f.Cells[0][1].Cont {
		t.Fatal("Text must not produce continuation cells")
	}
}
