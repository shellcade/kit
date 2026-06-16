package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestResolveArtifactPassthroughAndErrors pins the shared check/play/smoke
// argument contract: a .wasm passes through untouched (its directory is the
// game dir), and everything that is neither a .wasm nor a buildable game
// directory fails before any toolchain is invoked.
func TestResolveArtifactPassthroughAndErrors(t *testing.T) {
	wasm, dir, cleanup, err := resolveArtifact(filepath.Join("some", "path", "game.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if wasm != filepath.Join("some", "path", "game.wasm") {
		t.Errorf("a .wasm must pass through untouched, got %q", wasm)
	}
	if dir != filepath.Join("some", "path") {
		t.Errorf("game dir must be the artifact's directory, got %q", dir)
	}
	if cleanup != nil {
		t.Error("passthrough must not allocate a temp dir")
	}

	if _, _, _, err := resolveArtifact(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a nonexistent path must be refused")
	}

	plain := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveArtifact(plain); err == nil {
		t.Error("a non-.wasm regular file must be refused")
	}

	if _, _, _, err := resolveArtifact(t.TempDir()); err == nil {
		t.Error("a directory without go.mod or Cargo.toml must be refused")
	}
}

// TestCheckAcceptsGameDirectory is the dir-aware inner-loop contract for
// `check` (and, via the same resolveArtifact, `play`): pointing it at a game
// directory builds the artifact and runs the full harness. Requires tinygo;
// skipped when missing.
func TestCheckAcceptsGameDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tinygo"); err != nil {
		t.Skip("tinygo not on PATH")
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "paritygame"))
	if err != nil {
		t.Fatal(err)
	}
	if err := check(dir); err != nil {
		t.Fatalf("check <gamedir>: %v", err)
	}
}
