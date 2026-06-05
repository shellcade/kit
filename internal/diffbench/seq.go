// Package diffbench measures candidate frame-delta wire encodings for the
// shellcade guest->host frame channel against the current full-frame baseline.
//
// This is the ABI **v2** model: cells are 24-byte GRAPHEME CELLS and the frame
// payload is the v2 delta container (9-byte epoch header). A Frame is a fixed
// 24x80 cell grid; a full packed frame is 24*80*24 = 46080 bytes. The v2
// keyframe form (the bootstrap/full-frame member of the container) is
// 9 + 4 + 46080 = 46093 bytes. This package replays REAL captured frame
// sequences from catalog games (testdata/*.fseq, captured via kittest in the
// games repo) plus synthesized worst/edge sequences, and benchmarks the
// bytes-on-wire and encode cost of each candidate delta encoding.
//
// The committed .fseq testdata was captured against the round-1 16-byte cell
// (the catalog games are single-code-point today). loadSeq re-packs each
// 16-byte capture cell into the normative 24-byte v2 layout (cp2=cp3=pad=0):
// because the games use exactly one code point per cell, this widening is
// EXACT, not synthetic — the reconstructed 24-byte frames are the genuine v2
// production renders of those games.
//
// All numbers here are NATIVE Go. The guest runs under TinyGo/wasm, where
// absolute ns/op differ by a roughly constant factor (no SIMD memcmp, a
// simpler optimizer); the RELATIVE ordering and the byte counts (which are
// architecture-independent) carry over directly. Byte counts are the headline
// result and are exact for the wasm guest too.
package diffbench

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// Frame geometry. v2 normative cell layout (24 bytes, canonical-zero rule —
// unused cp slots and pad MUST be zero so cell equality == 24-byte memcmp):
//
//	u32 rune @0 | u32 cp2 @4 | u32 cp3 @8         (extra grapheme code points)
//	u8 fgSet,fgR,fgG,fgB @12 | u8 bgSet,bgR,bgG,bgB @16
//	u8 attr @20 | u8 cont @21 | u16 pad @22 (zero)
const (
	Rows       = 24
	Cols       = 80
	CellBytes  = 24
	FrameCells = Rows * Cols            // 1920
	FrameBytes = FrameCells * CellBytes // 46080
	RowBytes   = Cols * CellBytes       // 1920
)

// srcCellBytes is the round-1 capture cell width on disk; loadSeq widens each to
// the 24-byte v2 cell.
const srcCellBytes = 16

// Sequence is a list of fully-reconstructed packed frames (each FrameBytes).
type Sequence struct {
	Name   string
	Frames [][]byte
}

// widen16to24 re-packs one round-1 16-byte capture cell (src) into the
// normative 24-byte v2 cell (dst at offset o). The 16-byte source layout is
// u32 rune | u8 fgSet,fgR,fgG,fgB | u8 bgSet,bgR,bgG,bgB | u8 attr | u8 cont |
// u16 pad. The grapheme cp2/cp3 slots are 0 (the games are single-code-point
// today, so this is an EXACT widening) and pad is 0 (canonical-zero rule).
func widen16to24(src []byte, dst []byte, o int) {
	// rune (u32) @0
	binary.LittleEndian.PutUint32(dst[o:], binary.LittleEndian.Uint32(src))
	// cp2 @4, cp3 @8 = 0 (dst is zero-initialised; assert via explicit zero)
	binary.LittleEndian.PutUint32(dst[o+4:], 0)
	binary.LittleEndian.PutUint32(dst[o+8:], 0)
	// fg @12 <- src fg @4, bg @16 <- src bg @8
	copy(dst[o+12:o+16], src[4:8])
	copy(dst[o+16:o+20], src[8:12])
	// attr @20 <- src attr @12, cont @21 <- src cont @13
	dst[o+20] = src[12]
	dst[o+21] = src[13]
	// pad @22 (u16) = 0
	dst[o+22], dst[o+23] = 0, 0
}

// loadSeq reads a testdata/*.fseq file (the compact lossless delta format the
// capture harness wrote) and reconstructs the exact full packed frames, WIDENED
// to the 24-byte v2 cell (see widen16to24).
//
// Format: magic "FSEQ", u32 version=2, u32 frameCount, then per frame:
//
//	u16 changedCount, then changedCount * (u16 cellIndex + 16 packed bytes).
func loadSeq(path string) (*Sequence, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[:4]) != "FSEQ" {
		return nil, fmt.Errorf("%s: bad magic", path)
	}
	ver := binary.LittleEndian.Uint32(b[4:])
	if ver != 2 {
		return nil, fmt.Errorf("%s: unsupported version %d", path, ver)
	}
	count := int(binary.LittleEndian.Uint32(b[8:]))
	off := 12
	frames := make([][]byte, 0, count)
	prev := make([]byte, FrameBytes)
	for f := 0; f < count; f++ {
		if off+2 > len(b) {
			return nil, fmt.Errorf("%s: truncated at frame %d", path, f)
		}
		n := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		cur := make([]byte, FrameBytes)
		copy(cur, prev)
		for i := 0; i < n; i++ {
			if off+2+srcCellBytes > len(b) {
				return nil, fmt.Errorf("%s: truncated cell in frame %d", path, f)
			}
			idx := int(binary.LittleEndian.Uint16(b[off:]))
			off += 2
			widen16to24(b[off:off+srcCellBytes], cur, idx*CellBytes)
			off += srcCellBytes
		}
		frames = append(frames, cur)
		prev = cur
	}
	name := filepath.Base(path)
	name = name[:len(name)-len(filepath.Ext(name))]
	return &Sequence{Name: name, Frames: frames}, nil
}

// realScenarios loads every captured game sequence. Missing files are skipped
// (the bench still runs on synthetic scenarios), but a present file that fails
// to parse is fatal.
func realScenarios() ([]*Sequence, error) {
	var out []*Sequence
	for _, name := range []string{"tic-tac-toe", "chess", "blackjack", "pokies", "shellracer"} {
		p := filepath.Join("testdata", name+".fseq")
		if _, err := os.Stat(p); err != nil {
			continue
		}
		s, err := loadSeq(p)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ---- synthetic sequences ---------------------------------------------------

// blankFrame returns a packed frame of all-space cells (rune 0x20, no styling)
// — the canonical cleared frame a guest composes from Frame.Clear().
func blankFrame() []byte {
	f := make([]byte, FrameBytes)
	for i := 0; i < FrameCells; i++ {
		binary.LittleEndian.PutUint32(f[i*CellBytes:], uint32(' '))
	}
	return f
}

// putRune sets the base rune of cell i in a packed frame (cp2/cp3/styling left
// as-is). For single-code-point cells this is the whole grapheme.
func putRune(f []byte, i int, r rune) {
	binary.LittleEndian.PutUint32(f[i*CellBytes:], uint32(r))
}

// putGrapheme writes a multi-code-point grapheme cluster cell: base rune + up to
// two extra code points (VS16, skin-tone modifier, keycap, ZWJ piece) into the
// cp2/cp3 slots. Unused slots are zero (canonical-zero rule). It also marks the
// cell wide (cont=1 on the following cell) is the caller's job — this just packs
// the code points. Used by the grapheme-churn synthetic to exercise cp2/cp3
// actually non-zero (which the single-code-point captures never do).
func putGrapheme(f []byte, i int, base, cp2, cp3 rune) {
	o := i * CellBytes
	binary.LittleEndian.PutUint32(f[o:], uint32(base))
	binary.LittleEndian.PutUint32(f[o+4:], uint32(cp2))
	binary.LittleEndian.PutUint32(f[o+8:], uint32(cp3))
}

// synthWorstCase models a Clear + full redraw of entirely different content
// (e.g. a scene/screen transition): frame N and N+1 share no cell. Every one of
// the 1920 cells changes. This is the cliff the delta encodings must survive
// (and where falling back to a full frame matters).
func synthWorstCase() *Sequence {
	a := blankFrame()
	for i := 0; i < FrameCells; i++ {
		putRune(a, i, 'A')
	}
	b := blankFrame()
	for i := 0; i < FrameCells; i++ {
		putRune(b, i, 'B')
	}
	// Alternate A/B so each consecutive pair is a full change.
	frames := [][]byte{a, b, dup(a), dup(b), dup(a)}
	return &Sequence{Name: "synth-worstcase-fullchange", Frames: frames}
}

// synthStaticIdle models a long stretch where the guest re-sends an identical
// frame (the host coalesces, but the guest still pays the encode + ship). This
// isolates the SKIP-IDENTICAL win: the ideal encoding ships nothing.
func synthStaticIdle() *Sequence {
	base := blankFrame()
	putText(base, 1, 2, "Waiting for players...")
	frames := make([][]byte, 0, 12)
	for i := 0; i < 12; i++ {
		frames = append(frames, dup(base))
	}
	return &Sequence{Name: "synth-static-idle", Frames: frames}
}

// synthCursorBlink models a tiny localized update: a single cell toggling (a
// cursor/spinner), the best case for cell-list/run-list. ~1 changed cell/frame.
func synthCursorBlink() *Sequence {
	base := blankFrame()
	putText(base, 12, 4, "> enter your name: ")
	const cur = 12*Cols + 23
	frames := make([][]byte, 0, 16)
	for i := 0; i < 16; i++ {
		f := dup(base)
		if i%2 == 0 {
			putRune(f, cur, '_')
		}
		frames = append(frames, f)
	}
	return &Sequence{Name: "synth-cursor-blink", Frames: frames}
}

// synthScrollRow models a single full row changing each frame (a marquee /
// status line / scrolling log): 80 contiguous changed cells, the sweet spot
// for dirty-rows and run-list.
func synthScrollRow() *Sequence {
	base := blankFrame()
	putText(base, 0, 0, "STATUS")
	frames := make([][]byte, 0, 20)
	msg := "the quick brown fox jumps over the lazy dog while the band plays on and on"
	for i := 0; i < 20; i++ {
		f := dup(base)
		// shift the marquee one column per frame on row 23
		for c := 0; c < Cols; c++ {
			putRune(f, 23*Cols+c, rune(msg[(c+i)%len(msg)]))
		}
		frames = append(frames, f)
	}
	return &Sequence{Name: "synth-scroll-row", Frames: frames}
}

// synthHalfScreen models a moderate update: half the cells change each frame (a
// large animated panel / split layout redraw). ~960 changed cells — near the
// crossover where deltas stop paying.
func synthHalfScreen() *Sequence {
	frames := make([][]byte, 0, 8)
	for i := 0; i < 8; i++ {
		f := blankFrame()
		for r := 0; r < Rows; r++ {
			for c := 0; c < Cols; c++ {
				ch := ' '
				if r < Rows/2 {
					ch = rune('0' + (r+c+i)%10)
				}
				putRune(f, r*Cols+c, ch)
			}
		}
		frames = append(frames, f)
	}
	return &Sequence{Name: "synth-half-screen", Frames: frames}
}

// synthGraphemeChurn models a row of emoji grapheme clusters that mutate every
// frame — the v2-specific scenario that exercises cp2/cp3 ACTUALLY non-zero (the
// single-code-point catalog captures never touch them). Each emoji is a wide
// glyph: it occupies a base cell carrying the cluster code points plus a
// continuation cell (cont=1), per the width-authority convention. Per frame, the
// emoji on a 16-cell run cycle their variation selector / skin-tone modifier
// (the extra grapheme code points), so the changed cells carry non-zero cp2/cp3.
func synthGraphemeChurn() *Sequence {
	// Base emoji that take a VS16 (emoji presentation) and/or a skin-tone
	// modifier. We vary the cp2 (VS16 / skin tone) and cp3 (keycap) across
	// frames so deltas ship real multi-code-point cells.
	const vs16 = 0xFE0F                                            // VARIATION SELECTOR-16 (emoji presentation)
	const keycap = 0x20E3                                          // COMBINING ENCLOSING KEYCAP
	skin := []rune{0, 0x1F3FB, 0x1F3FC, 0x1F3FD, 0x1F3FE, 0x1F3FF} // none + 5 Fitzpatrick
	bases := []rune{0x261D /*☝*/, 0x270B /*✋*/, 0x1F44D /*👍*/, 0x1F44B /*👋*/, '1', '2', '3', '#'}

	const emojiRow = 6
	const emojiCount = 16 // 16 wide glyphs = 32 columns
	frames := make([][]byte, 0, 20)
	for fr := 0; fr < 20; fr++ {
		f := blankFrame()
		putText(f, emojiRow-2, 0, "grapheme churn: VS16 / skin-tone / keycap mutate per frame")
		for e := 0; e < emojiCount; e++ {
			base := bases[(e+fr)%len(bases)]
			var cp2, cp3 rune
			if base >= '0' && base <= '9' || base == '#' {
				// Keycap sequence: base + VS16 + COMBINING ENCLOSING KEYCAP.
				cp2, cp3 = vs16, keycap
			} else {
				// Emoji: VS16 plus a per-frame-cycling skin-tone modifier in cp3.
				cp2 = vs16
				cp3 = skin[(e+fr)%len(skin)]
			}
			col := e * 2
			cell := emojiRow*Cols + col
			putGrapheme(f, cell, base, cp2, cp3)
			// continuation cell for the wide glyph (cont=1), blank rune.
			contCell := cell + 1
			putRune(f, contCell, ' ')
			f[contCell*CellBytes+21] = 1 // cont byte @ offset 21
		}
		frames = append(frames, f)
	}
	return &Sequence{Name: "synth-grapheme-churn", Frames: frames}
}

func putText(f []byte, row, col int, s string) {
	for i, r := range s {
		if col+i >= Cols {
			break
		}
		putRune(f, row*Cols+col+i, r)
	}
}

func dup(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// synthScenarios returns the labelled synthetic sequences.
func synthScenarios() []*Sequence {
	return []*Sequence{
		synthStaticIdle(),
		synthCursorBlink(),
		synthScrollRow(),
		synthGraphemeChurn(),
		synthHalfScreen(),
		synthWorstCase(),
	}
}

// changedCells counts cells that differ between two packed frames.
func changedCells(prev, next []byte) int {
	n := 0
	for i := 0; i < FrameCells; i++ {
		o := i * CellBytes
		if !cellEqual(prev, next, o) {
			n++
		}
	}
	return n
}

// avgChangedCells reports the mean changed-cell count over a sequence's
// consecutive frame pairs (frame 0 diffed against a blank frame).
func avgChangedCells(s *Sequence) float64 {
	if len(s.Frames) == 0 {
		return 0
	}
	prev := blankFrame()
	total := 0
	for _, f := range s.Frames {
		total += changedCells(prev, f)
		prev = f
	}
	return float64(total) / float64(len(s.Frames))
}
