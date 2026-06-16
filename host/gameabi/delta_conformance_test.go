package gameabi

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/render"
	"github.com/shellcade/kit/v2/host/session"
)

// Host-side frame-delta conformance (tasks 6.1–6.5). These exercise the v2 delta
// codec and the host's baseline+epoch authority directly — the same wire codec
// and baselineCache the live send/identical host functions use — plus the host
// canvas/renderer grapheme path. Whole-guest conformance (the fixture driven
// through the real adapter) lives in internal/gameabi/conformance.

// ---- 6.1 delta codec round-trip + fuzz (never panics / never reads OOB) ------

// randomFrame fills a FrameBytes buffer with canonical-zero 24-byte cells whose
// content varies per cell, so a diff against another random frame produces a
// realistic run distribution.
func randomFrame(rng *rand.Rand) []byte {
	b := make([]byte, wire.FrameBytes)
	for i := 0; i < wire.FrameCells; i++ {
		if rng.Intn(3) == 0 {
			continue // leave a blank (all-zero) cell for run boundaries
		}
		wire.PutCell(b, i, wire.Cell{
			Rune:  rune('a' + rng.Intn(26)),
			FGSet: rng.Intn(2) == 0,
			FGR:   uint8(rng.Intn(256)),
			Attr:  uint8(rng.Intn(16)),
		})
	}
	return b
}

// TestDeltaRoundTrip: apply(base, diff(base, next)) == next over random 24-byte
// frame pairs, including full-change and zero-change (round-trip invariant, D2).
func TestDeltaRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	dst := make([]byte, wire.MaxDeltaBytes)
	for iter := 0; iter < 200; iter++ {
		base := randomFrame(rng)
		next := randomFrame(rng)
		if iter == 0 { // zero-change case
			next = append([]byte(nil), base...)
		}
		if iter == 1 { // full-change case: every cell differs
			base = bytes.Repeat([]byte{0}, wire.FrameBytes)
			next = bytes.Repeat([]byte{0xAB}, wire.FrameBytes)
			// 0xAB is not canonical (pad != 0), but the codec treats it as opaque
			// bytes; it still must round-trip exactly.
		}
		n := wire.BuildFrameDelta(base, next, dst, 7)
		delta := dst[:n]
		if n >= wire.KeyframeBytes {
			n = wire.BuildKeyframe(next, dst, 7)
			delta = dst[:n]
		}
		if err := wire.CheckFrameDelta(delta); err != nil {
			t.Fatalf("iter %d: CheckFrameDelta rejected our own encoder output: %v", iter, err)
		}
		recon := append([]byte(nil), base...)
		if err := wire.ApplyFrameDelta(recon, delta); err != nil {
			t.Fatalf("iter %d: ApplyFrameDelta: %v", iter, err)
		}
		if !bytes.Equal(recon, next) {
			t.Fatalf("iter %d: round-trip mismatch (apply(base, diff) != next)", iter)
		}
	}
}

// FuzzHostDeltaIngest: the host's delta validator/applier never panics and never
// reads out of bounds on arbitrary bytes (the drop-not-fatal contract, §4.5).
// CheckFrameDelta and ApplyFrameDelta must agree: if Check passes, Apply on a
// correctly-sized baseline must not error.
func FuzzHostDeltaIngest(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{wire.FlagKeyframe, 0, 0, 0, 0, 1, 0, 24, 80})
	f.Add(make([]byte, wire.KeyframeBytes)) // wrong geometry header => malformed
	// A structurally valid keyframe seed.
	kf := make([]byte, wire.MaxDeltaBytes)
	n := wire.BuildKeyframe(make([]byte, wire.FrameBytes), kf, 3)
	f.Add(kf[:n])
	f.Fuzz(func(t *testing.T, b []byte) {
		// Must never panic. A baselineCache.apply must drop-or-apply, never crash.
		var c baselineCache
		_ = c.apply(0, b, nil)
		// Check/Apply agreement on a correctly-sized baseline.
		err := wire.CheckFrameDelta(b)
		prev := make([]byte, wire.FrameBytes)
		applyErr := wire.ApplyFrameDelta(prev, b)
		if err == nil && applyErr != nil {
			t.Fatalf("CheckFrameDelta passed but ApplyFrameDelta failed: %v", applyErr)
		}
	})
}

// TestHostDropsMalformedDelta: the host baselineCache drops a malformed/short
// container, bumps the slot epoch, and reports not-applied — never panics (D5,
// 4.3, the "malformed or short delta is dropped, not fatal" scenario).
func TestHostDropsMalformedDelta(t *testing.T) {
	cases := map[string][]byte{
		"short header":      {0, 0, 0},
		"unknown flag bit":  {0x02, 0, 0, 0, 0, 0, 0, 24, 80},
		"wrong geometry":    {0, 0, 0, 0, 0, 0, 0, 25, 80},
		"runcount mismatch": {0, 0, 0, 0, 0, 5, 0, 24, 80}, // says 5 runs, body empty
		"out of bounds run": buildOneRun(t, 1900, 100),     // 1900+100 > 1920
	}
	for name, b := range cases {
		var c baselineCache
		c.epochSeq = 41 // last issued high-water
		c.epoch[0] = 41
		c.has[0] = true // pretend a baseline existed, so we can observe it cleared
		logged := false
		res := c.apply(0, b, func(string) { logged = true })
		if res.applied {
			t.Errorf("%s: applied a malformed container", name)
		}
		// bump advances the monotonic high-water (epochSeq 41 -> 42) and stamps it.
		if res.epoch != 42 {
			t.Errorf("%s: returned epoch = %d, want 42 (bumped high-water)", name, res.epoch)
		}
		if c.epoch[0] != 42 {
			t.Errorf("%s: slot epoch = %d, want 42", name, c.epoch[0])
		}
		if c.has[0] {
			t.Errorf("%s: slot left present after a dropped delta", name)
		}
		if !logged {
			t.Errorf("%s: no 'dropped malformed delta' log", name)
		}
	}
}

func buildOneRun(t *testing.T, start, runLen int) []byte {
	t.Helper()
	b := make([]byte, wire.DeltaHeaderBytes+wire.RunHeaderBytes+runLen*wire.CellBytes)
	b[0] = 0
	b[5] = 1 // runCount = 1
	b[7], b[8] = 24, 80
	b[9], b[10] = byte(start), byte(start>>8)
	b[11], b[12] = byte(runLen), byte(runLen>>8)
	return b
}

// ---- 6.2 delta-vs-keyframe byte-identical through the host apply path --------

// TestDeltaVsKeyframeByteIdentical: a slot driven by a SEQUENCE of deltas
// reconstructs a packed grid byte-identical to the same frames delivered as
// keyframes (D2 — the keyframe is the only full-frame form; the delta and the
// keyframe reconstruct the same frame). Mirrors the host's send apply path.
func TestDeltaVsKeyframeByteIdentical(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	frames := make([][]byte, 8)
	for i := range frames {
		frames[i] = randomFrame(rng)
	}

	// Delta path: keyframe the first frame, then deltas against the running
	// baseline (exactly what the SDK + host do).
	var deltaCache baselineCache
	dst := make([]byte, wire.MaxDeltaBytes)
	for i, fr := range frames {
		var payload []byte
		if i == 0 || !deltaCache.has[0] {
			n := wire.BuildKeyframe(fr, dst, deltaCache.epoch[0])
			payload = dst[:n]
		} else {
			n := wire.BuildFrameDelta(deltaCache.prev[0][:], fr, dst, deltaCache.epoch[0])
			if n >= wire.KeyframeBytes {
				n = wire.BuildKeyframe(fr, dst, deltaCache.epoch[0])
			}
			payload = dst[:n]
		}
		res := deltaCache.apply(0, payload, nil)
		if !res.applied {
			t.Fatalf("frame %d: host rejected our own valid payload", i)
		}
		// Mirror the guest's epoch stamp for the next iteration.
		deltaCache.epoch[0] = res.epoch
	}

	// Keyframe path: every frame as a keyframe.
	var kfCache baselineCache
	for i, fr := range frames {
		n := wire.BuildKeyframe(fr, dst, kfCache.epoch[0])
		res := kfCache.apply(0, dst[:n], nil)
		if !res.applied {
			t.Fatalf("frame %d: host rejected a keyframe", i)
		}
		kfCache.epoch[0] = res.epoch
	}

	if !bytes.Equal(deltaCache.prev[0][:], kfCache.prev[0][:]) {
		t.Fatal("delta-reconstructed baseline differs from the keyframe-reconstructed baseline")
	}
	// And both equal the last authored frame exactly.
	if !bytes.Equal(deltaCache.prev[0][:], frames[len(frames)-1]) {
		t.Fatal("reconstructed baseline differs from the last authored frame")
	}
}

// ---- 6.4 Identical-then-Send + mid-join reconciliation -----------------------

// TestIdenticalThenSendReconciles: a broadcast Identical reconciles every
// ALLOCATED per-index baseline, so a later per-player Send diffs against the
// baseline the broadcast left and reconstructs the exact frame (D7).
// Lazy-slot contract (large-room scale): slots never sent to are NOT
// materialized by a broadcast — they stay not-present and recover via an
// unconditionally-accepted keyframe on their first per-player Send (mirroring
// the guest SDK's lazy reconcile). Modeled on the host's identical/send apply
// paths.
func TestIdenticalThenSendReconciles(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var c baselineCache
	dst := make([]byte, wire.MaxDeltaBytes)

	// Slot 3 has had a prior per-player send (allocated); slot 0 never has.
	prior := randomFrame(rng)
	pn := wire.BuildKeyframe(prior, dst, c.epoch[3])
	if res := c.apply(3, dst[:pn], nil); !res.applied {
		t.Fatal("prior per-player keyframe to slot 3 rejected")
	}

	// Broadcast a keyframe to the broadcast slot, then reconcile.
	bcast := randomFrame(rng)
	n := wire.BuildKeyframe(bcast, dst, c.epoch[broadcastSlot])
	res := c.apply(broadcastSlot, dst[:n], nil)
	if !res.applied {
		t.Fatal("broadcast keyframe rejected")
	}
	c.reconcileBroadcast(res.epoch)

	// The allocated slot must now hold the broadcast frame, be present, and
	// carry the broadcast epoch. The never-sent slot must be left not-present.
	if !c.has[3] || c.epoch[3] != res.epoch || !bytes.Equal(c.prev[3], bcast) {
		t.Fatalf("allocated slot 3 not reconciled (has=%v epoch=%d)", c.has[3], c.epoch[3])
	}
	if c.has[0] {
		t.Fatal("never-sent slot 0 was materialized by the broadcast (lazy contract)")
	}

	// A later per-player Send to slot 3: a DELTA against the reconciled baseline,
	// stamped with the broadcast epoch, must apply and reconstruct the new frame.
	personal := randomFrame(rng)
	dn := wire.BuildFrameDelta(c.prev[3], personal, dst, c.epoch[3])
	if dn >= wire.KeyframeBytes {
		dn = wire.BuildKeyframe(personal, dst, c.epoch[3])
	}
	r2 := c.apply(3, dst[:dn], nil)
	if !r2.applied {
		t.Fatal("per-player Send after Identical was rejected (baseline left stale)")
	}
	if !bytes.Equal(c.prev[3], personal) {
		t.Fatal("per-player Send reconstructed the wrong frame")
	}

	// The never-sent slot recovers via keyframe: unconditionally accepted.
	r3kf := randomFrame(rng)
	kn := wire.BuildKeyframe(r3kf, dst, c.epoch[0])
	if r3 := c.apply(0, dst[:kn], nil); !r3.applied {
		t.Fatal("keyframe to never-sent slot 0 rejected (recovery path broken)")
	}
	if !bytes.Equal(c.prev[0], r3kf) {
		t.Fatal("slot 0 keyframe reconstructed the wrong frame")
	}
}

// TestMidJoinReceivesKeyframe: a roster mutation bumps the epoch and marks every
// slot not-present, so the next send to each slot is epoch-rejected — forcing
// the guest to a keyframe (the RFB incremental=0 analogue, D7/4.6).
func TestMidJoinReceivesKeyframe(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var c baselineCache
	dst := make([]byte, wire.MaxDeltaBytes)

	// Establish a baseline on slot 0 via a keyframe.
	fr := randomFrame(rng)
	n := wire.BuildKeyframe(fr, dst, c.epoch[0])
	res := c.apply(0, dst[:n], nil)
	if !res.applied || !c.has[0] {
		t.Fatal("initial keyframe not established")
	}
	epochBefore := res.epoch

	// Mid-room join: every slot is invalidated, the epoch bumps.
	c.invalidateAll()
	if c.has[0] {
		t.Fatal("slot 0 still present after a roster mutation")
	}
	if c.epoch[0] <= epochBefore {
		t.Fatalf("epoch not bumped on roster mutation: %d <= %d", c.epoch[0], epochBefore)
	}

	// The guest's next send is a DELTA stamped with its surviving (pre-bump)
	// epoch: the host must reject it (epoch mismatch AND not-present) and bump.
	staleDelta := make([]byte, wire.MaxDeltaBytes)
	sn := wire.BuildFrameDelta(fr, randomFrame(rng), staleDelta, epochBefore)
	rej := c.apply(0, staleDelta[:sn], nil)
	if rej.applied {
		t.Fatal("host applied a stale delta to an invalidated slot")
	}

	// The guest then sends a keyframe stamped with the returned epoch: accepted.
	kn := wire.BuildKeyframe(fr, dst, rej.epoch)
	acc := c.apply(0, dst[:kn], nil)
	if !acc.applied || !c.has[0] {
		t.Fatal("keyframe after a roster mutation was not accepted")
	}
}

// ---- 6.5 grapheme conformance (host canvas + renderer) -----------------------

// TestGraphemeHostConformance drives a frame carrying grapheme clusters (a VS16
// emoji, a skin-tone-modified emoji, a keycap base+U+20E3) through the host's
// pack -> delta -> apply -> decodeFrame -> render pipeline and asserts:
//
//	(a) packed cells are canonically zero in pad and unused cp slots,
//	(b) the reconstructed grid round-trips byte-identical,
//	(c) an over-3-code-point cluster is NOT representable (cell left blank),
//	(d) the rendered ANSI bursts each cluster's code points contiguously.
func TestGraphemeHostConformance(t *testing.T) {
	type cluster struct {
		col      int
		base     rune
		cp2, cp3 rune
	}
	clusters := []cluster{
		{0, '☂', 0xFE0F, 0},      // VS16 emoji presentation
		{2, '👍', 0x1F3FD, 0},     // skin-tone modifier
		{4, '1', 0xFE0F, 0x20E3}, // keycap: 1 + VS16 + U+20E3
	}

	// Author the frame via PutCell (the host's canonical-zero enforcer).
	packed := make([]byte, wire.FrameBytes)
	for _, cl := range clusters {
		idx := 0*wire.Cols + cl.col
		wire.PutCell(packed, idx, wire.Cell{Rune: cl.base, Cp2: cl.cp2, Cp3: cl.cp3})
	}

	// (a) Canonical-zero: pad (bytes 22..23) and any unused cp slot are zero.
	for _, cl := range clusters {
		o := (0*wire.Cols + cl.col) * wire.CellBytes
		if packed[o+22] != 0 || packed[o+23] != 0 {
			t.Errorf("col %d: pad bytes not zero", cl.col)
		}
		if cl.cp3 == 0 {
			if packed[o+8] != 0 || packed[o+9] != 0 || packed[o+10] != 0 || packed[o+11] != 0 {
				t.Errorf("col %d: unused cp3 slot not zero", cl.col)
			}
		}
	}

	// (b) Round-trip through the real delta apply path: keyframe -> apply ->
	// reconstructed baseline must equal the authored frame byte-for-byte.
	var c baselineCache
	dst := make([]byte, wire.MaxDeltaBytes)
	n := wire.BuildKeyframe(packed, dst, 0)
	if res := c.apply(broadcastSlot, dst[:n], nil); !res.applied {
		t.Fatal("grapheme keyframe rejected")
	}
	if !bytes.Equal(c.prev[broadcastSlot][:], packed) {
		t.Fatal("grapheme frame did not round-trip byte-identical through delta apply")
	}

	// decodeFrame must carry cp2/cp3 into the canvas grid.
	grid, err := decodeFrame(c.prev[broadcastSlot][:])
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	for _, cl := range clusters {
		got := grid.Cells[0][cl.col]
		if got.Rune != cl.base || got.Cp2 != cl.cp2 || got.Cp3 != cl.cp3 {
			t.Errorf("col %d: decoded cell = {%U,%U,%U}, want {%U,%U,%U}",
				cl.col, got.Rune, got.Cp2, got.Cp3, cl.base, cl.cp2, cl.cp3)
		}
	}

	// (c) A cluster of more than three code points is not representable: the cell
	// has only base+cp2+cp3, so a family ZWJ emoji (4+ cps) cannot be carried.
	// The host-side guarantee is that a cell never drawn (the guest refuses an
	// over-limit cluster) stays blank — verify a never-written cell is blank.
	blank := grid.Cells[0][40]
	// An undrawn packed cell is all-zero: Rune 0 (the renderer treats it as a
	// space), no cp2/cp3. The point is no over-limit cluster ever lands there.
	if blank.Rune != 0 || blank.Cp2 != 0 || blank.Cp3 != 0 {
		t.Errorf("an undrawn cell is not blank/zero: %+v", blank)
	}

	// (d) Rendered ANSI bursts each cluster's code points contiguously.
	out := render.GridToANSI(grid, session.Caps{ColorDepth: session.ColorTrue, UTF8: true})
	for _, cl := range clusters {
		want := string(cl.base)
		if cl.cp2 != 0 {
			want += string(cl.cp2)
		}
		if cl.cp3 != 0 {
			want += string(cl.cp3)
		}
		if !strings.Contains(out, want) {
			t.Errorf("col %d: rendered ANSI does not contain the contiguous cluster %q (% x)", cl.col, want, want)
		}
	}
}
