package main

// The wide-glyph width-contract lint. The SDK's wide-glyph writers
// — (*Frame).SetWide and (*Frame).SetGraphemeWide (see internal/game/grid.go
// ~128-173) — reserve TWO terminal columns for one glyph and never measure the
// glyph they are handed: width-2 is the AUTHOR's contract. If the base code
// point's East-Asian-Width is not Wide (W) or Fullwidth (F), the terminal
// advances the cursor by ONE column while the SDK reserved two, desyncing every
// column to that cell's right for the rest of the row. This is the bug that
// corrupted the pokies reels in production (a keycap base code point — EAW
// Neutral — fed to a wide writer; see the grapheme-width-contract lesson).
//
// `shellcade-kit lint-width <path>...` parses game source with go/parser, finds
// calls to the wide writers whose base code point is a determinable literal
// (a rune literal for SetWide, a string literal for SetGraphemeWide), and
// reports each base code point whose EAW is not W/F with file:line. It exits
// non-zero when any violation is found, so it is a one-command merge gate.
//
// Calls whose base code point is not a literal (a variable, a function result)
// are skipped — the lint reports only what it can prove, never guesses.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// wideWriters are the SDK method names whose first text argument (the third
// positional argument, after row, col) carries the base code point that MUST be
// EAW Wide or Fullwidth. SetWide takes a rune; SetGraphemeWide takes a cluster
// string whose FIRST code point is the base.
var wideWriters = map[string]struct{}{
	"SetWide":         {},
	"SetGraphemeWide": {},
}

// widthViolation is one offending wide-glyph call: the base code point that is
// neither EAW W nor F, located at file:line.
type widthViolation struct {
	File string
	Line int
	Fn   string // the writer name (SetWide / SetGraphemeWide)
	Base rune   // the offending base code point
	EAW  string // its East-Asian-Width class, for the report
}

func (v widthViolation) String() string {
	return fmt.Sprintf("%s:%d: %s base %s (East-Asian-Width %s) is not Wide or Fullwidth — a width-2 writer desyncs every column to its right",
		v.File, v.Line, v.Fn, describeRune(v.Base), v.EAW)
}

// describeRune renders a code point as U+XXXX 'x' for the report.
func describeRune(r rune) string {
	if strconv.IsPrint(r) {
		return fmt.Sprintf("U+%04X %q", r, r)
	}
	return fmt.Sprintf("U+%04X", r)
}

// lintWidth scans every Go source file reachable from the given paths (files are
// scanned directly; directories are walked) for wide-glyph width-contract
// violations, prints each to stdout, and returns an error (non-zero exit) when
// any violation was found.
func lintWidth(paths []string) error {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				if filepath.Ext(path) == ".go" {
					files = append(files, path)
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else if filepath.Ext(p) == ".go" {
			files = append(files, p)
		}
	}

	var violations []widthViolation
	for _, f := range files {
		vs, err := lintWidthFile(f)
		if err != nil {
			return err
		}
		violations = append(violations, vs...)
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	for _, v := range violations {
		fmt.Println(v)
	}
	if n := len(violations); n > 0 {
		return fmt.Errorf("%d wide-glyph width-contract violation(s)", n)
	}
	fmt.Printf("lint-width: OK — %d file(s) scanned, no width-contract violations\n", len(files))
	return nil
}

// lintWidthFile parses one Go file and returns its width-contract violations.
func lintWidthFile(path string) ([]widthViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var violations []widthViolation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, ok := wideWriters[sel.Sel.Name]; !ok {
			return true
		}
		// Signature is (row, col, glyph, style); the base code point is arg 2.
		if len(call.Args) < 3 {
			return true
		}
		base, ok := baseCodepoint(call.Args[2])
		if !ok {
			return true // not a determinable literal — report only what we can prove
		}
		if class, wide := eawClass(base); !wide {
			pos := fset.Position(call.Pos())
			violations = append(violations, widthViolation{
				File: path, Line: pos.Line, Fn: sel.Sel.Name, Base: base, EAW: class,
			})
		}
		return true
	})
	return violations, nil
}

// baseCodepoint extracts the base code point from a wide writer's glyph
// argument when it is a compile-time literal: a CHAR literal yields its rune,
// and a STRING literal yields its first decoded code point (the grapheme
// cluster's base). Returns ok=false for any non-literal expression.
func baseCodepoint(arg ast.Expr) (rune, bool) {
	lit, ok := arg.(*ast.BasicLit)
	if !ok {
		return 0, false
	}
	switch lit.Kind {
	case token.CHAR:
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return 0, false
		}
		for _, r := range s { // exactly one rune in a valid char literal
			return r, true
		}
		return 0, false
	case token.STRING:
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return 0, false
		}
		for _, r := range s { // first code point is the cluster base
			return r, true
		}
		return 0, false // empty string: no base
	}
	return 0, false
}
