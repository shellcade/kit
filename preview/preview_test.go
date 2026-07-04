package preview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// preview_test.go covers the bundle load/validate limits and the packer's
// pad-never-truncate contract.

// writeAuthoring lays down a valid 2-frame authoring dir and returns it.
func writeAuthoring(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		ManifestName: "frames:\n  - file: 01-a.frame\n    ms: 900\n  - file: 02-b.frame\n    ms: 400\n",
		"01-a.frame": "hello\nworld\n",
		"02-b.frame": "\x1b[31mred row\x1b[0m\nplain\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPackLoadRoundTrip(t *testing.T) {
	data, err := Pack(writeAuthoring(t))
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	anim, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(anim.Frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(anim.Frames))
	}
	if anim.Frames[0].MS != 900 || anim.Frames[1].MS != 400 {
		t.Fatalf("durations = %d/%d, want 900/400", anim.Frames[0].MS, anim.Frames[1].MS)
	}
	for i, f := range anim.Frames {
		if len(f.Rows) != Rows {
			t.Fatalf("frame %d rows = %d, want %d", i, len(f.Rows), Rows)
		}
	}
	if !strings.HasPrefix(anim.Frames[0].Rows[0], "hello ") {
		t.Fatalf("row 0 = %q, want padded %q", anim.Frames[0].Rows[0], "hello …")
	}
	// The styled row must close its styling before the padding.
	if !strings.Contains(anim.Frames[1].Rows[0], "\x1b[0m") {
		t.Fatalf("styled row lacks a closing reset: %q", anim.Frames[1].Rows[0])
	}
}

// packOne packs a single-frame authoring dir whose frame file holds body.
func packOne(t *testing.T, body string) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	manifest := "frames:\n  - file: f.frame\n    ms: 500\n"
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.frame"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Pack(dir)
}

func TestPackRejects(t *testing.T) {
	cases := []struct {
		name, body, wantErr string
	}{
		{"cursor escape", "ok\n\x1b[2;3Hmoved\n", "SGR"},
		{"unterminated escape", "bad\x1b[31\n", "unterminated"},
		{"control byte", "a\tb\n", "control character"},
		{"C1 CSI smuggle", "a\u009b2Jb\n", "control character"},
		{"raw C1 byte", "a\x9b2J\n", "invalid UTF-8"},
		{"keycap combiner", "press 7\ufe0f\u20e3 now\n", "glyph-width hazard"},
		{"zero-width joiner", "a\u200db\n", "glyph-width hazard"},
		{"too many rows", strings.Repeat("r\n", Rows+1), "rows"},
		{"oversize row", strings.Repeat("x", Cols+1) + "\n", "never truncates"},
		{"wide glyph overflow", strings.Repeat("x", Cols-1) + "🀄\n", "never truncates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := packOne(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "f.frame") {
				t.Fatalf("err %q does not name the offending file", err)
			}
		})
	}
}

func TestPackManifestErrors(t *testing.T) {
	write := func(t *testing.T, manifest string, frames map[string]string) error {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		for name, body := range frames {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Pack(dir)
		return err
	}
	if err := write(t, "frames:\n  - file: missing.frame\n    ms: 100\n", nil); err == nil ||
		!strings.Contains(err.Error(), "missing.frame") {
		t.Fatalf("missing frame file: err = %v", err)
	}
	if err := write(t, "frames:\n  - file: f.frame\n", map[string]string{"f.frame": "x\n"}); err == nil ||
		!strings.Contains(err.Error(), "missing ms") {
		t.Fatalf("missing ms: err = %v", err)
	}
	if err := write(t, "frames:\n  - ms: 100\n", nil); err == nil ||
		!strings.Contains(err.Error(), "missing file") {
		t.Fatalf("missing file field: err = %v", err)
	}
	if err := write(t, "frames: []\n", nil); err == nil ||
		!strings.Contains(err.Error(), "0 frames") {
		t.Fatalf("empty manifest: err = %v", err)
	}
}

// bundle builds a valid single-frame bundle, then lets a test mutate it.
func bundle(t *testing.T, mutate func(*bundleJSON)) []byte {
	t.Helper()
	row := strings.Repeat(" ", Cols)
	lines := make([]string, Rows)
	for i := range lines {
		lines[i] = row
	}
	b := bundleJSON{V: Version, Frames: []frameJSON{{MS: 500, Lines: lines}}}
	if mutate != nil {
		mutate(&b)
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLoadValidates(t *testing.T) {
	if _, err := Load(bundle(t, nil)); err != nil {
		t.Fatalf("valid bundle: %v", err)
	}
	cases := []struct {
		name    string
		mutate  func(*bundleJSON)
		wantErr string
	}{
		{"future version", func(b *bundleJSON) { b.V = 2 }, "version 2"},
		{"no frames", func(b *bundleJSON) { b.Frames = nil }, "0 frames"},
		{"short frame", func(b *bundleJSON) { b.Frames[0].Lines = b.Frames[0].Lines[:3] }, "3 rows"},
		{"narrow row", func(b *bundleJSON) { b.Frames[0].Lines[2] = "short" }, "cells"},
		{"cursor escape", func(b *bundleJSON) {
			b.Frames[0].Lines[0] = "\x1b[2J" + strings.Repeat(" ", Cols)
		}, "SGR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(bundle(t, tc.mutate))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
	if _, err := Load([]byte("{not json")); err == nil {
		t.Fatal("garbage bundle loaded")
	}
	if _, err := Load(make([]byte, MaxBundleBytes+1)); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Fatal("oversize bundle loaded")
	}
}

func TestLoadClampsDurations(t *testing.T) {
	anim, err := Load(bundle(t, func(b *bundleJSON) { b.Frames[0].MS = 10 }))
	if err != nil {
		t.Fatal(err)
	}
	if anim.Frames[0].MS != MinFrameMS {
		t.Fatalf("MS = %d, want clamped %d", anim.Frames[0].MS, MinFrameMS)
	}
	anim, err = Load(bundle(t, func(b *bundleJSON) { b.Frames[0].MS = 99999 }))
	if err != nil {
		t.Fatal(err)
	}
	if anim.Frames[0].MS != MaxFrameMS {
		t.Fatalf("MS = %d, want clamped %d", anim.Frames[0].MS, MaxFrameMS)
	}
}
