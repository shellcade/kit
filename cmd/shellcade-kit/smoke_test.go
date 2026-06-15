package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	kit "github.com/shellcade/kit/v2"
	kitsmoke "github.com/shellcade/kit/v2/smoke"

	"github.com/shellcade/kit/v2/host/canvas"
)

// TestGridToKitFrameRendersIdentically proves the conversion is faithful by
// building the same screen twice — once on the host canvas, once on a kit
// frame — and asserting kit's canonical encoder emits identical bytes.
func TestGridToKitFrameRendersIdentically(t *testing.T) {
	g := canvas.New()
	red := canvas.RGB(255, 0, 0)
	g.Set(0, 0, canvas.Cell{Rune: 'h', FG: red, Attr: canvas.AttrBold | canvas.AttrUnderline})
	g.Set(0, 1, canvas.Cell{Rune: 'i', BG: canvas.RGB(0, 10, 20)})
	g.Set(1, 0, canvas.Cell{Rune: '❤', Cp2: 0xFE0F})              // VS16 grapheme
	g.Set(2, 0, canvas.Cell{Rune: '7', Cp2: 0xFE0F, Cp3: 0x20E3}) // keycap
	g.Set(3, 0, canvas.Cell{Rune: '個', Attr: canvas.AttrDim})
	g.Set(3, 1, canvas.Cell{Cont: true})

	f := kit.NewFrame()
	f.Set(0, 0, kit.Cell{Rune: 'h', FG: kit.RGB(255, 0, 0), Attr: kit.AttrBold | kit.AttrUnderline})
	f.Set(0, 1, kit.Cell{Rune: 'i', BG: kit.RGB(0, 10, 20)})
	f.Set(1, 0, kit.Cell{Rune: '❤', Cp2: 0xFE0F})
	f.Set(2, 0, kit.Cell{Rune: '7', Cp2: 0xFE0F, Cp3: 0x20E3})
	f.Set(3, 0, kit.Cell{Rune: '個', Attr: kit.AttrDim})
	f.Set(3, 1, kit.Cell{Cont: true})

	conv := gridToKitFrame(g)
	if got, want := kitsmoke.RenderANSI(conv), kitsmoke.RenderANSI(f); !bytes.Equal(got, want) {
		t.Fatalf("converted grid renders differently\n got: %q\nwant: %q", got, want)
	}
	if got, want := kitsmoke.RenderText(conv), kitsmoke.RenderText(f); !bytes.Equal(got, want) {
		t.Fatalf("text twin differs\n got: %q\nwant: %q", got, want)
	}
}

// TestNativeWasmParity is the contract test: the same smoke.yaml against the
// same game source must produce byte-identical shot files from the native
// runner (`go run . -smoke`) and the wasm path (`shellcade-kit smoke`).
// Requires go + tinygo; skipped when either is missing.
func TestNativeWasmParity(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	for _, tool := range []string{"go", "tinygo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	gameDir, err := filepath.Abs("testdata/paritygame")
	if err != nil {
		t.Fatal(err)
	}

	// Native shots via the game's own dev runner.
	nativeOut := filepath.Join(t.TempDir(), "native")
	cmd := exec.Command("go", "run", ".", "-smoke", "smoke.yaml", "-smoke-out", nativeOut)
	cmd.Dir = gameDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go run . -smoke: %v\n%s", err, out)
	}

	// Wasm shots through the conformance harness (the runSmoke path).
	wasm := filepath.Join(t.TempDir(), "game.wasm")
	cmd = exec.Command("tinygo", "build", "-opt=1", "-no-debug", "-gc=conservative",
		"-o", wasm, "-target", "wasip1", "-buildmode=c-shared", ".")
	cmd.Dir = gameDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tinygo build: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(gameDir, "smoke.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sc, err := kitsmoke.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	shots, err := runWasmSmoke(wasm, sc)
	if err != nil {
		t.Fatal(err)
	}
	wasmOut := filepath.Join(t.TempDir(), "wasm")
	if _, err := kitsmoke.WriteShots(wasmOut, shots); err != nil {
		t.Fatal(err)
	}

	// Same file set, byte-identical contents.
	nativeFiles, _ := filepath.Glob(filepath.Join(nativeOut, "*"))
	wasmFiles, _ := filepath.Glob(filepath.Join(wasmOut, "*"))
	if len(nativeFiles) == 0 || len(nativeFiles) != len(wasmFiles) {
		t.Fatalf("file sets differ: native %d, wasm %d", len(nativeFiles), len(wasmFiles))
	}
	for _, nf := range nativeFiles {
		name := filepath.Base(nf)
		nb, err := os.ReadFile(nf)
		if err != nil {
			t.Fatal(err)
		}
		wb, err := os.ReadFile(filepath.Join(wasmOut, name))
		if err != nil {
			t.Fatalf("wasm path missing %s: %v", name, err)
		}
		if !bytes.Equal(nb, wb) {
			t.Fatalf("%s differs between native and wasm paths\nnative: %q\nwasm:   %q", name, nb, wb)
		}
	}
}
