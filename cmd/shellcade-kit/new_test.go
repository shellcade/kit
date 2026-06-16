package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	kitsmoke "github.com/shellcade/kit/v2/smoke"
)

// chtemp runs the scaffolder from a temp cwd (scaffold writes ./<name>/).
func chtemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestScaffoldRustEmitsPinnedTagAndArtifactPath(t *testing.T) {
	chtemp(t)
	if err := runNew("My-Game", true, "MIT"); err != nil {
		t.Fatal(err)
	}

	// Expected file set, lowercased name.
	for _, f := range []string{"Cargo.toml", "src/lib.rs", "README.md"} {
		if _, err := os.Stat(filepath.Join("my-game", f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}

	cargo := read(t, "my-game/Cargo.toml")
	// Drift-proof pin: the cargo git dep carries the exact kit version this
	// binary was built against (same mechanism as the Go /v2 module pin).
	wantTag := `tag = "` + kitVersion() + `"`
	if !strings.Contains(cargo, wantTag) {
		t.Errorf("Cargo.toml must pin %s, got:\n%s", wantTag, cargo)
	}
	if !strings.Contains(cargo, `git = "https://github.com/shellcade/kit"`) {
		t.Error("Cargo.toml must depend on the kit repo by git URL")
	}
	// The artifact contract: cdylib leaf + the FULL release profile (cargo
	// ignores a dependency's profiles — this block must live in the game).
	for _, want := range []string{
		`crate-type = ["cdylib", "rlib"]`,
		`opt-level = "s"`, "lto = true", "strip = true", `panic = "abort"`,
	} {
		if !strings.Contains(cargo, want) {
			t.Errorf("Cargo.toml missing %q", want)
		}
	}

	lib := read(t, "my-game/src/lib.rs")
	if !strings.Contains(lib, "#![forbid(unsafe_code)]") {
		t.Error("scaffolded game must forbid unsafe_code")
	}
	if !strings.Contains(lib, `slug: "my-game"`) {
		t.Error("meta slug must be the lowercased name")
	}
	if !strings.Contains(lib, "shellcade_game!(TheGame)") {
		t.Error("scaffold must register via shellcade_game!")
	}

	// The exact underscored artifact path is baked into README and lib.rs —
	// never left as folklore.
	readme := read(t, "my-game/README.md")
	const artifact = "target/wasm32-wasip1/release/my_game.wasm"
	if !strings.Contains(readme, artifact) {
		t.Errorf("README must carry the exact artifact path %s", artifact)
	}
	if !strings.Contains(lib, artifact) {
		t.Errorf("lib.rs header must carry the exact artifact path %s", artifact)
	}
}

func TestScaffoldRustRefusesBadNamesAndExisting(t *testing.T) {
	chtemp(t)
	if err := runNew("has space", true, "MIT"); err == nil {
		t.Error("name with space must be refused")
	}
	if err := runNew("ok-name", true, "MIT"); err != nil {
		t.Fatal(err)
	}
	if err := runNew("ok-name", true, "MIT"); err == nil {
		t.Error("existing directory must be refused")
	}
}

func TestScaffoldGoStillEmitsModulePin(t *testing.T) {
	chtemp(t)
	if err := runNew("gogame", false, "MIT"); err != nil {
		t.Fatal(err)
	}
	gomod := read(t, "gogame/go.mod")
	if !strings.Contains(gomod, "github.com/shellcade/kit/v2 "+kitVersion()) {
		t.Errorf("go.mod must pin the kit module version, got:\n%s", gomod)
	}
}

// TestScaffoldEmitsCatalogRequiredFiles asserts both scaffolds are
// catalog-submittable out of the box: smoke.yaml (parsing under the kit
// schema, with at least one shot) and a LICENSE the games-repo CI validator
// recognizes from its first five lines.
func TestScaffoldEmitsCatalogRequiredFiles(t *testing.T) {
	chtemp(t)
	if err := runNew("go-gate", false, "MIT"); err != nil {
		t.Fatal(err)
	}
	if err := runNew("rust-gate", true, "MIT"); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"go-gate", "rust-gate"} {
		sc, err := kitsmoke.Parse([]byte(read(t, dir+"/smoke.yaml")))
		if err != nil {
			t.Fatalf("%s/smoke.yaml does not parse under the kit smoke schema: %v", dir, err)
		}
		if sc.Seats < 1 {
			t.Errorf("%s/smoke.yaml: want at least one seat, got %d", dir, sc.Seats)
		}
		shots := 0
		for _, st := range sc.Steps {
			if st.Kind == kitsmoke.StepShot {
				shots++
			}
		}
		if shots < 2 {
			t.Errorf("%s/smoke.yaml: want at least two shots (a working example), got %d", dir, shots)
		}

		lic := read(t, dir+"/LICENSE")
		head := strings.Join(strings.SplitN(lic, "\n", 6)[:5], "\n")
		if !strings.Contains(head, "MIT License") {
			t.Errorf("%s/LICENSE head must carry the MIT title line for the CI validator, got:\n%s", dir, head)
		}
		if !strings.Contains(lic, "the "+dir+" authors") {
			t.Errorf("%s/LICENSE must name a copyright holder, got:\n%s", dir, head)
		}
	}
}

// TestScaffoldLicenseFlagCoversTheCatalogAllowlist drives --license through
// every allowlisted SPDX id and asserts the emitted LICENSE matches the SAME
// first-five-lines patterns the games-repo CI validator
// (validate_game_dir.py) applies — and that an off-list id is refused with
// the allowlist named.
func TestScaffoldLicenseFlagCoversTheCatalogAllowlist(t *testing.T) {
	chtemp(t)
	validator := map[string]*regexp.Regexp{
		"MIT":          regexp.MustCompile(`(?i)MIT License`),
		"Apache-2.0":   regexp.MustCompile(`(?i)Apache License\s*$|Apache License,? Version 2\.0`),
		"BSD-3-Clause": regexp.MustCompile(`(?i)BSD 3-Clause`),
		"MPL-2.0":      regexp.MustCompile(`(?i)Mozilla Public License,? (Version )?2\.0`),
		"Unlicense":    regexp.MustCompile(`(?i)free and unencumbered software`),
	}
	if len(validator) != len(licenseIDs()) {
		t.Fatalf("allowlist drift: CLI offers %v, test mirrors %d validator patterns", licenseIDs(), len(validator))
	}
	for i, id := range licenseIDs() {
		pat, ok := validator[id]
		if !ok {
			t.Errorf("--license %s is not in the catalog CI allowlist", id)
			continue
		}
		dir := fmt.Sprintf("lic-%d", i)
		if err := runNew(dir, false, id); err != nil {
			t.Fatalf("runNew --license %s: %v", id, err)
		}
		head := strings.Join(strings.SplitN(read(t, dir+"/LICENSE"), "\n", 6)[:5], "\n")
		if !pat.MatchString(head) {
			t.Errorf("--license %s: first five lines do not match the CI validator pattern %s:\n%s", id, pat, head)
		}
	}
	// Case-insensitive ids are accepted; off-list ids are refused loudly.
	if err := runNew("lic-lower", false, "mit"); err != nil {
		t.Errorf("--license mit (case-insensitive) must be accepted: %v", err)
	}
	err := runNew("lic-bogus", false, "GPL-3.0")
	if err == nil {
		t.Fatal("--license GPL-3.0 (off the catalog allowlist) must be refused")
	}
	if !strings.Contains(err.Error(), "MIT") || !strings.Contains(err.Error(), "Unlicense") {
		t.Errorf("the refusal must name the allowlist, got: %v", err)
	}
	if _, statErr := os.Stat("lic-bogus"); statErr == nil {
		t.Error("a refused scaffold must not leave a directory behind")
	}
}

// TestScaffoldNameValidationMatchesThePlatform asserts `new` applies the
// host's own bareName rule (^[a-z0-9-]{1,32}$) at scaffold time, so an
// invalid slug fails in the first minute instead of at the first `check`
// after a game has been built around it.
func TestScaffoldNameValidationMatchesThePlatform(t *testing.T) {
	chtemp(t)
	for _, bad := range []string{
		"my_game",               // underscore
		"my.game",               // dot
		"name!",                 // punctuation
		strings.Repeat("a", 33), // over-long
		"", "spa ce", "sla/sh",  // the previously-caught cases still fail
	} {
		for _, rust := range []bool{false, true} {
			if err := runNew(bad, rust, "MIT"); err == nil {
				t.Errorf("name %q (rust=%v) must be refused at scaffold time", bad, rust)
			}
		}
	}
	// Upper case is folded, not refused (existing behavior); the result is a
	// valid platform slug.
	if err := runNew("Upper-Case", false, "MIT"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("upper-case"); err != nil {
		t.Errorf("scaffold must land in the lowercased directory: %v", err)
	}
}
