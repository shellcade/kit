package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEAWClass pins the EAW judgement at the contract boundary: a code point is
// a legal wide-writer base iff it is W or F, and the pokies offenders (ASCII
// digit, keycap base) are correctly single-column.
func TestEAWClass(t *testing.T) {
	cases := []struct {
		name string
		r    rune
		want bool // is W or F (a legal wide base)
	}{
		{"ASCII seven (the keycap base that corrupted pokies)", '7', false},
		{"ASCII A", 'A', false},
		{"halfwidth katakana", 0xFF61, false},
		{"CJK 個", '個', true},
		{"fullwidth seven ７", '７', true},
		{"umbrella ☔", '☔', true},
		{"heart ❤", '❤', true},
		{"gem 💎", '\U0001F48E', true},
		{"CJK Ext B", '\U00020000', true},
		{"low boundary just below Hangul Jamo", 0x10FF, false},
		{"Hangul Jamo low boundary", 0x1100, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, wide := eawClass(c.r); wide != c.want {
				t.Errorf("eawClass(%U) wide=%v, want %v", c.r, wide, c.want)
			}
		})
	}
}

// TestEAWTableSorted guards the binary-search invariant: eaw2cols must be
// strictly ascending and non-overlapping, or eawClass would silently misclassify.
func TestEAWTableSorted(t *testing.T) {
	for i := 1; i < len(eaw2cols); i++ {
		prev, cur := eaw2cols[i-1], eaw2cols[i]
		if prev.lo > prev.hi {
			t.Errorf("range %d is inverted: %U..%U", i-1, prev.lo, prev.hi)
		}
		if cur.lo <= prev.hi {
			t.Errorf("ranges %d and %d overlap or are out of order: %U..%U then %U..%U",
				i-1, i, prev.lo, prev.hi, cur.lo, cur.hi)
		}
	}
}

// TestLintWidthFile is the table-driven file-level test: the passing fixture
// yields zero violations, the failing fixture flags exactly the bad bases.
func TestLintWidthFile(t *testing.T) {
	cases := []struct {
		fixture   string
		wantBases []rune // expected offending base code points, in source order
	}{
		{"testdata/widthlint/pass.go.txt", nil},
		{"testdata/widthlint/fail.go.txt", []rune{'7', 'A', '7', 'x'}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			vs, err := lintWidthFile(c.fixture)
			if err != nil {
				t.Fatalf("lintWidthFile(%s): %v", c.fixture, err)
			}
			if len(vs) != len(c.wantBases) {
				t.Fatalf("got %d violations %v, want %d", len(vs), vs, len(c.wantBases))
			}
			for i, want := range c.wantBases {
				if vs[i].Base != want {
					t.Errorf("violation %d base = %U, want %U", i, vs[i].Base, want)
				}
				if vs[i].Line == 0 {
					t.Errorf("violation %d has no line number", i)
				}
			}
		})
	}
}

// TestLintWidthExitsNonZeroOnViolation drives the top-level entry point exactly
// as the CLI dispatch does: a clean tree returns nil, a dirty tree returns an
// error (the non-zero exit), and directory walking finds the .go file.
func TestLintWidthExitsNonZeroOnViolation(t *testing.T) {
	clean := writeGo(t, `package g
import "github.com/shellcade/kit/v2"
func d(f *kit.Frame) { f.SetWide(0, 0, '個', kit.Style{}) }`)
	if err := lintWidth([]string{clean}); err != nil {
		t.Fatalf("clean dir should pass, got %v", err)
	}

	dirty := writeGo(t, `package g
import "github.com/shellcade/kit/v2"
func d(f *kit.Frame) { f.SetWide(0, 0, '7', kit.Style{}) }`)
	if err := lintWidth([]string{dirty}); err == nil {
		t.Fatal("dirty dir should fail (non-zero exit), got nil")
	}
}

// writeGo writes src to a real .go file in a fresh temp dir and returns the dir,
// so lintWidth's directory walk (which filters on the .go extension) sees it.
func writeGo(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "game.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
