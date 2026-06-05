package kittest_test

// Public ABI-v2 conformance for the author-facing surface: it drives a fixture
// game through the kittest double, then runs the frame-delta codec and the
// grapheme cell through the public `wire` package exactly as a game author (or a
// from-scratch non-Go guest reasoning from ABI.md §4.5) would. It deliberately
// uses only the exported kit/wire/kittest API — no internal packages — so it
// also guards the public surface from regressing.

import (
	"bytes"
	"math/rand"
	"testing"

	kit "github.com/shellcade/kit/v2"
	"github.com/shellcade/kit/v2/kittest"
	"github.com/shellcade/kit/v2/wire"
)

// ---- a fixture game that draws single- and multi-code-point cells ------------

type graphemeGame struct{}

func (graphemeGame) Meta() kit.GameMeta {
	return kit.GameMeta{Slug: "conformance-grapheme", Name: "Conformance", MinPlayers: 1, MaxPlayers: 2}
}
func (graphemeGame) NewRoom(kit.RoomConfig, kit.Services) kit.Handler { return &graphemeHandler{} }

type graphemeHandler struct{ kit.Base }

// draw composes the fixture frame: a title plus a row of grapheme clusters
// (VS16, keycap, skin-tone) and an over-limit family ZWJ emoji that must refuse.
func (graphemeHandler) draw(f *kit.Frame) {
	f.Clear()
	f.Text(0, 0, "CONFORMANCE", kit.Style{Attr: kit.AttrBold})
	col := 0
	col = f.SetGrapheme(2, col, "❤️", kit.Style{FG: kit.Red})     // VS16     (2 cps)
	col = f.SetGrapheme(2, col, "1️⃣", kit.Style{})                // keycap   (3 cps)
	col = f.SetGrapheme(2, col, "👍🏽", kit.Style{FG: kit.Green})    // skintone (2 cps)
	// A family ZWJ emoji (5 code points) must be refused: col stays put.
	if next := f.SetGrapheme(2, col, "👨‍👩‍👧", kit.Style{}); next != col {
		panic("SetGrapheme accepted a >3-code-point cluster")
	}
}

func (h graphemeHandler) OnJoin(r kit.Room, p kit.Player) {
	f := kit.NewFrame()
	h.draw(f)
	r.Send(p, f)
}

func (h graphemeHandler) OnWake(r kit.Room) {
	f := kit.NewFrame()
	h.draw(f)
	r.Identical(f)
}

// ---- helpers -----------------------------------------------------------------

// pack encodes a captured *kit.Frame to the canonical 24-byte-cell wire bytes,
// the form the SDK diffs and the host reconstructs.
func pack(f *kit.Frame) []byte {
	buf := make([]byte, wire.FrameBytes)
	for row := 0; row < kit.Rows; row++ {
		for c := 0; c < kit.Cols; c++ {
			cell := f.Cells[row][c]
			wire.PutCell(buf, row*kit.Cols+c, wire.Cell{
				Rune: cell.Rune, Cp2: cell.Cp2, Cp3: cell.Cp3,
				FGSet: cell.FG.IsSet(), BGSet: cell.BG.IsSet(),
				Attr: uint8(cell.Attr), Cont: cell.Cont,
			})
		}
	}
	return buf
}

// applyVia builds the wire payload base->next (keyframe when the budget rule
// trips) and reconstructs it onto a copy of base, the host's apply path.
func applyVia(t *testing.T, base, next []byte, epoch uint32) []byte {
	t.Helper()
	dst := make([]byte, wire.MaxDeltaBytes)
	n := wire.BuildFrameDelta(base, next, dst, epoch)
	if n >= wire.KeyframeBytes {
		n = wire.BuildKeyframe(next, dst, epoch)
	}
	payload := append([]byte(nil), dst[:n]...)
	if err := wire.CheckFrameDelta(payload); err != nil {
		t.Fatalf("CheckFrameDelta rejected a self-built container: %v", err)
	}
	out := append([]byte(nil), base...)
	if err := wire.ApplyFrameDelta(out, payload); err != nil {
		t.Fatalf("ApplyFrameDelta: %v", err)
	}
	return out
}

// ---- conformance: codec round-trip over the public wire API ------------------

func TestConformanceDeltaRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	mkFrame := func(fill float64) []byte {
		buf := make([]byte, wire.FrameBytes)
		for i := 0; i < wire.FrameCells; i++ {
			c := wire.Cell{Rune: ' '}
			if rng.Float64() < fill {
				c.Rune = rune('A' + rng.Intn(26))
				if rng.Float64() < 0.25 {
					c.Cp2 = 0xFE0F // VS16
				}
			}
			wire.PutCell(buf, i, c)
		}
		return buf
	}
	fills := []float64{0.0, 0.005, 0.05, 0.5, 1.0} // incl. zero- and full-change
	for iter := 0; iter < 100; iter++ {
		base := mkFrame(fills[rng.Intn(len(fills))])
		next := mkFrame(fills[rng.Intn(len(fills))])
		if out := applyVia(t, base, next, uint32(iter)); !bytes.Equal(out, next) {
			t.Fatalf("iter %d: apply(base, diff(base,next)) != next", iter)
		}
	}
}

// TestConformanceDecoderNeverPanics is the public-surface fuzz target: the
// validator and apply path must never panic or read out of bounds on arbitrary
// bytes, and must agree on acceptance (the drop-not-fatal contract).
func TestConformanceDecoderNeverPanics(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	prev := make([]byte, wire.FrameBytes)
	for i := 0; i < 5000; i++ {
		b := make([]byte, rng.Intn(64))
		rng.Read(b)
		checkErr := wire.CheckFrameDelta(b)
		applyErr := wire.ApplyFrameDelta(append([]byte(nil), prev...), b)
		if (checkErr == nil) != (applyErr == nil) {
			t.Fatalf("Check/Apply disagree on %x: check=%v apply=%v", b, checkErr, applyErr)
		}
	}
}

// ---- conformance: delta run is byte-identical to a keyframe control ----------

func TestConformanceDeltaMatchesKeyframeControl(t *testing.T) {
	r := kittest.NewRoom(kittest.Player("p1"))
	h := graphemeGame{}.NewRoom(r.Config(), r.Services())
	h.OnStart(r)
	h.OnJoin(r, r.Players[0]) // first frame
	for i := 0; i < 3; i++ {
		h.OnWake(r) // re-broadcast the same frame
	}
	frames := r.Frames[r.Players[0].AccountID]
	if len(frames) < 2 {
		t.Fatalf("want >=2 captured frames, got %d", len(frames))
	}

	// Reconstruct every captured frame via the delta path against a rolling
	// baseline, and assert it is byte-identical to the keyframe control (the
	// frame packed directly). This is exactly the host's apply invariant.
	baseline := make([]byte, wire.FrameBytes)
	var epoch uint32
	for i, f := range frames {
		control := pack(f)
		dst := make([]byte, wire.MaxDeltaBytes)
		var payload []byte
		if i == 0 {
			payload = dst[:wire.BuildKeyframe(control, dst, epoch)] // first send: keyframe
		} else {
			n := wire.BuildFrameDelta(baseline, control, dst, epoch)
			if n >= wire.KeyframeBytes {
				n = wire.BuildKeyframe(control, dst, epoch)
			}
			payload = dst[:n]
		}
		if err := wire.ApplyFrameDelta(baseline, payload); err != nil {
			t.Fatalf("frame %d: apply: %v", i, err)
		}
		if !bytes.Equal(baseline, control) {
			t.Fatalf("frame %d: delta reconstruction != keyframe control", i)
		}
	}
}

// ---- conformance: the grapheme case (a) canonical-zero (b) round-trip
// (c) >3-cp refusal (d) contiguous render burst --------------------------------

func TestConformanceGraphemeCase(t *testing.T) {
	r := kittest.NewRoom(kittest.Player("p1"))
	h := graphemeGame{}.NewRoom(r.Config(), r.Services())
	h.OnStart(r)
	h.OnJoin(r, r.Players[0]) // (c) refusal is asserted inside draw() (panics otherwise)

	f := r.LastFrame(r.Players[0])
	if f == nil {
		t.Fatal("no frame captured")
	}
	// Locate the three grapheme cells written at row 2, cols 0..2.
	heart := f.Cells[2][0]
	keycap := f.Cells[2][1]
	thumb := f.Cells[2][2]
	if heart.Rune != '❤' || heart.Cp2 != 0xFE0F || heart.Cp3 != 0 {
		t.Fatalf("VS16 cell wrong: %+v", heart)
	}
	if keycap.Rune != '1' || keycap.Cp2 != 0xFE0F || keycap.Cp3 != 0x20E3 {
		t.Fatalf("keycap cell wrong: %+v", keycap)
	}
	if thumb.Rune != '\U0001F44D' || thumb.Cp2 != '\U0001F3FD' || thumb.Cp3 != 0 {
		t.Fatalf("skin-tone cell wrong: %+v", thumb)
	}

	packed := pack(f)

	// (a) Packed cells are canonically zero in pad and unused cp slots. The heart
	// cell (index 160) uses cp2 only -> cp3 (@8..11) and pad (@22..23) zero.
	heartOff := (2*kit.Cols + 0) * wire.CellBytes
	for _, rel := range []int{8, 9, 10, 11, 22, 23} {
		if packed[heartOff+rel] != 0 {
			t.Fatalf("heart cell byte +%d not canonical zero: %d", rel, packed[heartOff+rel])
		}
	}

	// A perturbation in any of the 24 bytes (including pad) registers as a change
	// — canonical-zero is load-bearing for delta determinism, not a nicety.
	perturbed := append([]byte(nil), packed...)
	perturbed[heartOff+23] = 0x01 // a stray bit in pad
	dst := make([]byte, wire.MaxDeltaBytes)
	if n := wire.BuildFrameDelta(packed, perturbed, dst, 0); n == wire.DeltaHeaderBytes {
		t.Fatal("a pad-byte perturbation did not register as a changed cell")
	}

	// (b) The reconstructed grid round-trips byte-identical via a keyframe.
	got := make([]byte, wire.FrameBytes)
	if err := wire.ApplyFrameDelta(got, dst[:wire.BuildKeyframe(packed, dst, 0)]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, packed) {
		t.Fatal("keyframe round-trip not byte-identical for the grapheme frame")
	}
	// And GetCell reads back the same code points it was given.
	if rc := wire.GetCell(got, 2*kit.Cols+1); rc.Rune != '1' || rc.Cp2 != 0xFE0F || rc.Cp3 != 0x20E3 {
		t.Fatalf("GetCell did not round-trip the keycap: %+v", rc)
	}

	// (d) A renderer must burst base+cp2+cp3 contiguously. The public surface does
	// not export the arcade renderer, so we assert the contract the renderer
	// implements over the public cell fields: base then cp2 then cp3, adjacent.
	burst := graphemeBurst(keycap)
	if burst != "1️⃣" {
		t.Fatalf("grapheme code points not contiguous: %q", burst)
	}
}

// graphemeBurst is the cluster-burst contract every conformant renderer must
// honor (ABI.md §4.3 / host §3): emit the base, then cp2 if non-zero, then cp3
// if non-zero, with no separator, before advancing to the next cell.
func graphemeBurst(c kit.Cell) string {
	out := []rune{c.Rune}
	if c.Cp2 != 0 {
		out = append(out, c.Cp2)
		if c.Cp3 != 0 {
			out = append(out, c.Cp3)
		}
	}
	return string(out)
}

// ---- conformance: a single-code-point game is unaffected ---------------------

func TestConformanceSingleCodePointUnchanged(t *testing.T) {
	f := kit.NewFrame()
	f.Text(0, 0, "plain ascii", kit.Style{})
	packed := pack(f)
	// Every cell's cp2/cp3 slots and pad are zero.
	for i := 0; i < wire.FrameCells; i++ {
		o := i * wire.CellBytes
		for _, rel := range []int{4, 5, 6, 7, 8, 9, 10, 11, 22, 23} {
			if packed[o+rel] != 0 {
				t.Fatalf("cell %d byte +%d not zero for single-code-point content", i, rel)
			}
		}
	}
}
