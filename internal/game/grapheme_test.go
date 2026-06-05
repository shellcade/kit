package game

import "testing"

func TestSetGraphemeWritesUpToThreeCodePoints(t *testing.T) {
	f := NewFrame()
	st := Style{FG: Green}

	// VS16 emoji: base + variation selector (2 code points).
	next := f.SetGrapheme(1, 2, "❤️", st) // ❤️
	if next != 3 {
		t.Fatalf("next col = %d, want 3", next)
	}
	c := f.Cells[1][2]
	if c.Rune != '❤' || c.Cp2 != '️' || c.Cp3 != 0 {
		t.Fatalf("VS16 cell wrong: %+v", c)
	}

	// Keycap: '1' + U+FE0F + U+20E3 (3 code points).
	f.SetGrapheme(1, 5, "1️⃣", st) // 1️⃣
	c = f.Cells[1][5]
	if c.Rune != '1' || c.Cp2 != '️' || c.Cp3 != '⃣' {
		t.Fatalf("keycap cell wrong: %+v", c)
	}

	// Skin-tone-modified emoji: base + modifier (2 code points).
	f.SetGrapheme(1, 8, "\U0001f44d\U0001f3fd", st) // 👍🏽
	c = f.Cells[1][8]
	if c.Rune != '\U0001f44d' || c.Cp2 != '\U0001f3fd' || c.Cp3 != 0 {
		t.Fatalf("skin-tone cell wrong: %+v", c)
	}
}

func TestSetGraphemeRefusesOverThreeCodePoints(t *testing.T) {
	f := NewFrame()
	before := f.Cells[0][0]
	// Family ZWJ emoji: man + ZWJ + woman + ZWJ + girl = 5 code points.
	next := f.SetGrapheme(0, 0, "\U0001f468‍\U0001f469‍\U0001f467", Style{})
	if next != 0 {
		t.Fatalf("over-limit cluster should return col unchanged, got %d", next)
	}
	if f.Cells[0][0] != before {
		t.Fatal("over-limit cluster must draw nothing")
	}
}

func TestSetGraphemeRefusesEmptyCluster(t *testing.T) {
	f := NewFrame()
	before := f.Cells[0][0]
	if next := f.SetGrapheme(0, 0, "", Style{}); next != 0 {
		t.Fatalf("empty cluster should return col unchanged, got %d", next)
	}
	if f.Cells[0][0] != before {
		t.Fatal("empty cluster must draw nothing")
	}
}

func TestSetGraphemeWideWritesContinuation(t *testing.T) {
	f := NewFrame()
	st := Style{BG: Cyan}
	next := f.SetGraphemeWide(3, 4, "❤️", st)
	if next != 6 {
		t.Fatalf("next col = %d, want 6", next)
	}
	lead := f.Cells[3][4]
	if lead.Rune != '❤' || lead.Cp2 != '️' || lead.Cont {
		t.Fatalf("lead cell wrong: %+v", lead)
	}
	cont := f.Cells[3][5]
	if !cont.Cont || cont.Rune != 0 || cont.Cp2 != 0 {
		t.Fatalf("continuation cell wrong: %+v", cont)
	}
}

func TestSetGraphemeWideRefusesAtRightEdge(t *testing.T) {
	f := NewFrame()
	before := f.Cells[0][Cols-1]
	if next := f.SetGraphemeWide(0, Cols-1, "❤️", Style{}); next != Cols-1 {
		t.Fatalf("right-edge wide grapheme should return col unchanged, got %d", next)
	}
	if f.Cells[0][Cols-1] != before {
		t.Fatal("right-edge wide grapheme must draw nothing")
	}
}

func TestSetGraphemeWideRefusesOverThreeCodePoints(t *testing.T) {
	f := NewFrame()
	before4, before5 := f.Cells[0][4], f.Cells[0][5]
	next := f.SetGraphemeWide(0, 4, "\U0001f468‍\U0001f469‍\U0001f467", Style{})
	if next != 4 {
		t.Fatalf("over-limit wide cluster should return col unchanged, got %d", next)
	}
	if f.Cells[0][4] != before4 || f.Cells[0][5] != before5 {
		t.Fatal("over-limit wide cluster must draw nothing")
	}
}

// Canonical-zero round-trip: a packed grapheme cell reads back identically and
// keeps pad/unused slots zero.
func TestGraphemePacksCanonicalZero(t *testing.T) {
	f := NewFrame()
	f.SetGrapheme(0, 0, "❤️", Style{}) // cp3 unused
	packed := encodeFrame(f)
	// Cell 0: cp3 slot @8..11 must be zero, pad @22..23 zero.
	for _, off := range []int{8, 9, 10, 11, 22, 23} {
		if packed[off] != 0 {
			t.Fatalf("cell 0 byte %d not canonical zero: %d", off, packed[off])
		}
	}
}
