package smoke

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shellcade/kit/v2/internal/game"
)

// RenderANSI renders a frame to the shot file form: 24 truecolor ANSI lines,
// LF separators, trailing newline. This is the canonical byte form — the
// devkit CLI's wasm path renders through the same encoder.
func RenderANSI(f *game.Frame) []byte {
	return []byte(strings.Join(game.ANSIRows(f), "\n") + "\n")
}

// RenderText renders a frame as plain text: full grapheme clusters, no escape
// sequences, trailing blanks trimmed — the greppable twin of RenderANSI.
func RenderText(f *game.Frame) []byte {
	return []byte(strings.Join(game.TextRows(f), "\n") + "\n")
}

// WriteShots writes each shot's .ansi and .txt files into dir (created if
// missing): NN-<name>.{ansi,txt} when the shot collapses (single seat, or all
// captured frames identical), NN-<name>.seat<K>.{ansi,txt} otherwise.
// It returns the written file names, in order.
func WriteShots(dir string, shots []Shot) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("smoke: %w", err)
	}
	var names []string
	write := func(name string, b []byte) error {
		names = append(names, name)
		return os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	for i := range shots {
		s := &shots[i]
		stem := fmt.Sprintf("%02d-%s", s.Ordinal, s.Name)
		if s.Collapsed() {
			if err := write(stem+".ansi", RenderANSI(s.Frames[0])); err != nil {
				return nil, err
			}
			if err := write(stem+".txt", RenderText(s.Frames[0])); err != nil {
				return nil, err
			}
			continue
		}
		for j, seat := range s.Seats {
			ss := fmt.Sprintf("%s.seat%d", stem, seat)
			if err := write(ss+".ansi", RenderANSI(s.Frames[j])); err != nil {
				return nil, err
			}
			if err := write(ss+".txt", RenderText(s.Frames[j])); err != nil {
				return nil, err
			}
		}
	}
	return names, nil
}
