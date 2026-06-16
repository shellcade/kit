package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveArtifact turns a check/play/smoke argument into (wasm path, game
// dir): a .wasm is used as-is; a directory is built per its module marker —
// go.mod via the pinned TinyGo profile, Cargo.toml via cargo wasm32-wasip1 —
// into a temp artifact (cleanup removes it). This is the whole-toolchain
// inner loop in one command: `shellcade-kit play .` (or check/smoke) builds
// and runs the real artifact regardless of the game's source language.
func resolveArtifact(arg string) (wasm, dir string, cleanup func(), err error) {
	if strings.HasSuffix(arg, ".wasm") {
		return arg, filepath.Dir(arg), nil, nil
	}
	info, err := os.Stat(arg)
	if err != nil {
		return "", "", nil, err
	}
	if !info.IsDir() {
		return "", "", nil, fmt.Errorf("%s is neither a .wasm nor a game directory", arg)
	}
	dir = arg

	tmp, err := os.MkdirTemp("", "shellcade-kit-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	wasm = filepath.Join(tmp, "game.wasm")

	var cmd *exec.Cmd
	switch {
	case exists(filepath.Join(dir, "go.mod")):
		// The pinned TinyGo profile — the same build games CI runs.
		cmd = exec.Command("tinygo", "build", "-opt=1", "-no-debug", "-gc=conservative",
			"-o", wasm, "-target", "wasip1", "-buildmode=c-shared", ".")
	case exists(filepath.Join(dir, "Cargo.toml")):
		cmd = exec.Command("cargo", "build", "--release", "--target", "wasm32-wasip1")
	default:
		cleanup()
		return "", "", nil, fmt.Errorf("%s has neither go.mod nor Cargo.toml", dir)
	}
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("build %s: %w", dir, err)
	}

	if exists(filepath.Join(dir, "Cargo.toml")) {
		// cargo writes into target/; find the produced cdylib.
		matches, _ := filepath.Glob(filepath.Join(dir, "target", "wasm32-wasip1", "release", "*.wasm"))
		if len(matches) != 1 {
			cleanup()
			return "", "", nil, fmt.Errorf("expected exactly one wasm under target/wasm32-wasip1/release, found %d", len(matches))
		}
		wasm = matches[0]
	}
	return wasm, dir, cleanup, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
