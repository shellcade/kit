// Package diffbench measures candidate frame-delta wire encodings for the
// shellcade guest->host frame channel against the current full-frame baseline.
//
// A Frame is a fixed 24x80 cell grid; the packed wire encoding is 24*80*16 =
// 30720 bytes (ABI.md §4.3). Today every send ships the full 30720-byte
// payload. This package replays REAL captured frame sequences from catalog
// games (testdata/*.fseq, captured via kittest in the games repo) plus
// synthesized worst/edge sequences, and benchmarks the bytes-on-wire and
// encode cost of each candidate delta encoding.
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

// Frame geometry (mirrors wire.* — duplicated so the bench has no dependency on
// internal encode details and operates purely on the packed wire bytes).
const (
	Rows       = 24
	Cols       = 80
	CellBytes  = 16
	FrameCells = Rows * Cols // 1920
	FrameBytes = FrameCells * CellBytes
	RowBytes   = Cols * CellBytes // 1280
)

// Sequence is a list of fully-reconstructed packed frames (each FrameBytes).
type Sequence struct {
	Name   string
	Frames [][]byte
}

// loadSeq reads a testdata/*.fseq file (the compact lossless delta format the
// capture harness wrote) and reconstructs the exact full packed frames.
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
			if off+2+CellBytes > len(b) {
				return nil, fmt.Errorf("%s: truncated cell in frame %d", path, f)
			}
			idx := int(binary.LittleEndian.Uint16(b[off:]))
			off += 2
			copy(cur[idx*CellBytes:idx*CellBytes+CellBytes], b[off:off+CellBytes])
			off += CellBytes
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
	for _, name := range []string{"tic-tac-toe", "blackjack", "pokies", "shellracer"} {
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

// putRune sets the rune of cell i in a packed frame (styling left as-is).
func putRune(f []byte, i int, r rune) {
	binary.LittleEndian.PutUint32(f[i*CellBytes:], uint32(r))
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
