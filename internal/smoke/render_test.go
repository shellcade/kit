package smoke

import (
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/internal/game"
)

func TestRenderANSIStylesAndShape(t *testing.T) {
	f := game.NewFrame()
	f.Text(0, 0, "hi", game.Style{FG: game.RGB(255, 0, 0), Attr: game.AttrBold})
	out := string(RenderANSI(f))

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != game.Rows {
		t.Fatalf("lines: %d, want %d", len(lines), game.Rows)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("want trailing newline")
	}
	if !strings.Contains(lines[0], "\x1b[0;1;38;2;255;0;0mhi") {
		t.Fatalf("row 0 SGR: %q", lines[0])
	}
	for i, l := range lines {
		if !strings.HasSuffix(l, "\x1b[0m") {
			t.Fatalf("row %d missing reset: %q", i, l)
		}
	}
	if strings.Contains(out, "\r") {
		t.Fatal("shot files use LF, not CRLF")
	}
}

func TestRenderANSIGraphemeBurst(t *testing.T) {
	f := game.NewFrame()
	// ❤️ = U+2764 U+FE0F (VS16): cluster must be emitted unbroken.
	f.SetGrapheme(2, 4, "❤️", game.Style{})
	out := string(RenderANSI(f))
	if !strings.Contains(out, "❤️") {
		t.Fatalf("grapheme cluster not burst into output")
	}
}

func TestRenderANSIWideContinuation(t *testing.T) {
	f := game.NewFrame()
	f.SetWide(0, 0, '個', game.Style{})
	line := strings.SplitN(string(RenderANSI(f)), "\n", 2)[0]
	// The wide rune occupies two columns but emits once: 1 wide rune + 78
	// spaces = 79 emitted glyphs.
	plain := strings.NewReplacer("\x1b[0m", "", "\x1b[0;", "", "m", "").Replace(line)
	if !strings.HasPrefix(plain, "個 ") {
		t.Fatalf("wide rune emission: %q", plain[:8])
	}
	if n := len([]rune(stripSGR(line))); n != game.Cols-1 {
		t.Fatalf("emitted glyphs: %d, want %d (continuation cell skipped)", n, game.Cols-1)
	}
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestRenderTextTrimsAndKeepsGraphemes(t *testing.T) {
	f := game.NewFrame()
	f.Text(0, 0, "score", game.Style{FG: game.Red})
	f.SetGrapheme(0, 6, "7️⃣", game.Style{}) // keycap: 3 code points
	out := string(RenderText(f))
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != game.Rows {
		t.Fatalf("lines: %d", len(lines))
	}
	if lines[0] != "score 7️⃣" {
		t.Fatalf("row 0: %q", lines[0])
	}
	for i, l := range lines[1:] {
		if l != "" {
			t.Fatalf("row %d not trimmed: %q", i+1, l)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Fatal("text render must carry no escapes")
	}
}
