package gameabi

import (
	"errors"
	"log/slog"
	"math/rand"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// replayRoom is a minimal, deterministic sdk.Room the snapshot tests drive a
// handler against directly: a fixed roster + a settable clock, recording every
// frame the guest pushes (broadcast or personal). Unlike TestRoom it never runs
// a join/start callback of its own, so a restored handler can resume mid-script
// with no extra guest calls — the control and the restored continuation see the
// exact same callback sequence, making frame equality a true determinism check.
type replayRoom struct {
	roster []sdk.Player
	cfg    sdk.RoomConfig
	clock  time.Time
	log    *slog.Logger
	svc    sdk.Services

	frames []sdk.Frame // every frame, in push order (broadcast counts once)
	ended  bool
	res    sdk.Result
	ctx    sdk.InputContext
}

func newReplayRoom(roster []sdk.Player, cfg sdk.RoomConfig, clock time.Time) *replayRoom {
	return &replayRoom{roster: roster, cfg: cfg, clock: clock, log: quietLog()}
}

func (r *replayRoom) Members() []sdk.Player { return append([]sdk.Player(nil), r.roster...) }
func (r *replayRoom) Has(p sdk.Player) bool {
	for _, m := range r.roster {
		if m == p {
			return true
		}
	}
	return false
}
func (r *replayRoom) Count() int             { return len(r.roster) }
func (r *replayRoom) Config() sdk.RoomConfig { return r.cfg }
func (r *replayRoom) Rand() *rand.Rand       { return rand.New(rand.NewSource(r.cfg.Seed)) }
func (r *replayRoom) Now() time.Time         { return r.clock }

func (r *replayRoom) Send(p sdk.Player, f sdk.Frame) { r.frames = append(r.frames, f) }
func (r *replayRoom) Identical(f sdk.Frame)          { r.frames = append(r.frames, f) }
func (r *replayRoom) BroadcastFunc(compose func(p sdk.Player) sdk.Frame) {
	for _, p := range r.roster {
		r.frames = append(r.frames, compose(p))
	}
}

func (r *replayRoom) After(time.Duration, func(sdk.Room)) sdk.TimerID { return 0 }
func (r *replayRoom) Every(time.Duration, func(sdk.Room)) sdk.TimerID { return 0 }
func (r *replayRoom) Cancel(sdk.TimerID)                              {}
func (r *replayRoom) SetSimRate(time.Duration)                        {}
func (r *replayRoom) SetFrameRate(time.Duration)                      {}
func (r *replayRoom) SetPhase(string, bool, time.Time)                {}
func (r *replayRoom) SetInputContext(ctx sdk.InputContext)            { r.ctx = ctx }

func (r *replayRoom) End(res sdk.Result) {
	if r.ended {
		return
	}
	r.ended, r.res = true, res
}
func (r *replayRoom) Result() (sdk.Result, bool) {
	if r.ended {
		return r.res, true
	}
	return sdk.Result{}, false
}
func (r *replayRoom) Services() sdk.Services { return r.svc }
func (r *replayRoom) Log() *slog.Logger      { return r.log }

// ---- round trip --------------------------------------------------------------

// snapScript is a deterministic continuation the control and the restored room
// each run: a few wakes, an entropy draw, an input-context cycle, a personal
// frame. Time advances per step so the guest clock moves like a live room.
func snapScript(h *wasmHandler, r *replayRoom, who sdk.Player) {
	for i := 0; i < 3; i++ {
		r.clock = r.clock.Add(50 * time.Millisecond)
		h.OnTick(r, r.clock)
	}
	h.OnInput(r, who, runeIn('r')) // draw entropy
	h.OnInput(r, who, runeIn('i')) // cycle input context
	h.OnInput(r, who, runeIn('f')) // personal frame
	r.clock = r.clock.Add(50 * time.Millisecond)
	h.OnTick(r, r.clock)
}

// TestSnapshotRoundTrip plays the fixture to a checkpoint, snapshots it, restores
// into a FRESH handler, and runs an identical continuation on both an
// uninterrupted control and the restored room. The continuation frames must be
// byte-identical — proving memory + entropy-stream + clock + roster all resume.
func TestSnapshotRoundTrip(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 12345, SeedSet: true}
	roster := []sdk.Player{p1}
	start := time.Unix(1_700_000_000, 0)

	// --- prefix: drive a handler to a checkpoint (start, join, wakes, entropy) ---
	prefix := func(h *wasmHandler) *replayRoom {
		r := newReplayRoom(roster, cfg, start)
		h.OnStart(r)             // instantiate + start
		h.OnJoin(r, p1)          // join the member
		for i := 0; i < 4; i++ { // some wakes to advance the wake counter
			r.clock = r.clock.Add(50 * time.Millisecond)
			h.OnTick(r, r.clock)
		}
		h.OnInput(r, p1, runeIn('r')) // draw entropy (advances the stream)
		h.OnInput(r, p1, runeIn('i')) // set input context
		return r
	}

	// Control: one handler runs prefix THEN continuation, uninterrupted.
	hCtl := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	rCtl := prefix(hCtl)
	checkpointClock := rCtl.clock
	rCtl.frames = nil // compare only the continuation
	snapScript(hCtl, rCtl, p1)
	wantFrames := rCtl.frames

	// Snapshot path: a second handler runs the SAME prefix, snapshots at the
	// checkpoint, then a restored handler runs the continuation.
	hA := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	rA := prefix(hA)
	if rA.clock != checkpointClock {
		t.Fatalf("prefix nondeterministic clock: %v vs %v", rA.clock, checkpointClock)
	}
	blob, err := hA.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	t.Logf("snapshot blob: %d bytes (compressed)", len(blob))

	hB, err := g.Restore(blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(hB.roster) != 1 || hB.roster[0] != p1 {
		t.Fatalf("restored roster = %+v, want [p1]", hB.roster)
	}
	if hB.inputCtx != hA.inputCtx {
		t.Fatalf("restored input ctx = %v, want %v", hB.inputCtx, hA.inputCtx)
	}
	if hB.nowNanos != hA.nowNanos {
		t.Fatalf("restored clock = %d, want %d", hB.nowNanos, hA.nowNanos)
	}

	rB := newReplayRoom(roster, cfg, checkpointClock)
	// ABI v2 resync (D6): the host's per-consumer baseline cache is ephemeral and
	// is NOT snapshotted, so on resume it re-seeds the epoch above the snapshot
	// high-water and marks every slot not-present. The restored guest's surviving
	// baseline makes its first send a DELTA, which the host epoch-rejects — and
	// the SDK immediately retries the same frame as a keyframe in the same call
	// (kit >= v2.0.1), so NO frame is dropped and no priming is needed: the
	// restored room renders the SAME continuation as the uninterrupted control,
	// frame-for-frame, byte-for-byte (canonical-zero cells).
	hB.OnResume(rB)
	snapScript(hB, rB, p1)
	gotFrames := rB.frames

	if len(gotFrames) != len(wantFrames) {
		t.Fatalf("frame count: restored %d, control %d", len(gotFrames), len(wantFrames))
	}
	for i := range wantFrames {
		if !framesEqual(wantFrames[i], gotFrames[i]) {
			t.Fatalf("continuation frame %d differs between control and restored room", i)
		}
	}
}

// TestRestoreRejectsMismatch: Restore verifies the artifact hash + ABI version.
// A tampered artifact-hash byte makes Restore refuse the blob.
func TestRestoreRejectsMismatch(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	r := newReplayRoom([]sdk.Player{p1}, cfg, time.Unix(1_700_000_000, 0))
	h.OnStart(r)
	h.OnJoin(r, p1)
	blob, err := h.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Corrupt the decompressed artifact hash, re-compress, and expect a refusal.
	raw, err := zstdDecode(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	st, _, err := decodeSnapshot(raw)
	if err != nil {
		t.Fatalf("decodeSnapshot: %v", err)
	}
	if st.artifact != g.wasmSHA {
		t.Fatal("snapshot did not record the artifact hash")
	}
	// Flip a hash byte at its fixed offset (after magic+format+abi = 12 bytes).
	raw[12] ^= 0xff
	_, err = g.Restore(zstdEncode(raw))
	if err == nil {
		t.Fatal("Restore accepted a blob with a mismatched artifact hash")
	}
	// The refusal is the TYPED sentinel: callers (lobby resume, resident
	// bring-up) tell "the live version moved under this snapshot" apart from
	// genuine corruption and surface "retired by a game update" instead.
	if !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("mismatch error does not wrap ErrArtifactMismatch: %v", err)
	}
}

// TestCloseHandlerReleasesUnadoptedRestore: RestoreHandler returns a handler
// holding a LIVE instance with grown, written linear memory; before a runtime
// adopts it (sdk.NewRoomRuntime), CloseHandler is the only disposal path — the
// guard every Restore call site applies on its error/lost-race returns so an
// unadopted handler never pins up to 32MiB in the game's shared wazero runtime.
func TestCloseHandlerReleasesUnadoptedRestore(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	r := newReplayRoom([]sdk.Player{p1}, cfg, time.Unix(1_700_000_000, 0))
	h.OnStart(r)
	h.OnJoin(r, p1)
	blob, err := h.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored, err := RestoreHandler(g, blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if GuestMemorySize(restored) == 0 {
		t.Fatal("restored handler reports no live linear memory — the leak premise changed")
	}
	if !CloseHandler(restored) {
		t.Fatal("CloseHandler refused a wasm handler")
	}
	if got := GuestMemorySize(restored); got != 0 {
		t.Fatalf("instance still live after CloseHandler: %d bytes", got)
	}
	if !CloseHandler(restored) {
		t.Fatal("CloseHandler is not idempotent on an already-closed handler")
	}
	if CloseHandler(sdk.Base{}) {
		t.Fatal("CloseHandler claimed to close a non-wasm handler")
	}
}

// framesEqual is the byte-identity check: the 80x24 cell grid is a comparable
// fixed-size array, so equality is exact.
func framesEqual(a, b sdk.Frame) bool { return a.Cells == b.Cells }

// ---- leaderboard post sequence (format 4) --------------------------------------

// postCapture records every leaderboard post the host issues, with the
// host-stamped room-scoped RoundSeq.
type postCapture struct {
	slugs []string
	seqs  []uint64
}

func (c *postCapture) Post(slug string, r sdk.Result) {
	c.slugs = append(c.slugs, slug)
	c.seqs = append(c.seqs, r.RoundSeq)
}

// TestSnapshotPostSeqIdempotency: the host stamps each leaderboard post with a
// monotonic room-scoped sequence, and the counter survives a snapshot/restore
// (format 4). A room reclaimed from a PRE-settle checkpoint that re-settles
// the same round must replay the SAME sequence the control issued — that
// sequence is what the durable leaderboard derives the idempotent round id
// from, so the re-settle dedupes instead of double-counting.
func TestSnapshotPostSeqIdempotency(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 99, SeedSet: true}
	roster := []sdk.Player{p1}
	start := time.Unix(1_700_000_000, 0)

	// Control: post once, checkpoint here, then settle (post again).
	ctl := &postCapture{}
	hCtl := g.NewRoom(cfg, sdk.Services{Log: quietLog(), Leaderboard: ctl}).(*wasmHandler)
	rCtl := newReplayRoom(roster, cfg, start)
	hCtl.OnStart(rCtl)
	hCtl.OnJoin(rCtl, p1)
	hCtl.OnInput(rCtl, p1, runeIn('s')) // fixture posts a result
	if hCtl.postSeq != 1 {
		t.Fatalf("postSeq=%d want 1 after first post", hCtl.postSeq)
	}
	blob, err := hCtl.Snapshot() // pre-settle checkpoint
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	hCtl.OnInput(rCtl, p1, runeIn('s')) // the settle the crash will replay
	if want := []uint64{1, 2}; len(ctl.seqs) != 2 || ctl.seqs[0] != want[0] || ctl.seqs[1] != want[1] {
		t.Fatalf("control seqs=%v want %v", ctl.seqs, want)
	}

	// Reclaim: restore the pre-settle checkpoint and re-settle. The replayed
	// post must carry the control's sequence (2), not restart at 1.
	hRe, err := g.Restore(blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer func() { _ = CloseHandler(hRe) }()
	if hRe.postSeq != 1 {
		t.Fatalf("restored postSeq=%d want 1 (format 4 must carry the counter)", hRe.postSeq)
	}
	re := &postCapture{}
	if !BindServices(hRe, sdk.Services{Log: quietLog(), Leaderboard: re}) {
		t.Fatal("BindServices refused the restored handler")
	}
	rRe := newReplayRoom(roster, cfg, rCtl.clock)
	hRe.OnResume(rRe)
	hRe.OnInput(rRe, p1, runeIn('s'))
	if len(re.seqs) != 1 || re.seqs[0] != 2 {
		t.Fatalf("replayed seqs=%v want [2] (same round -> same sequence -> same round id)", re.seqs)
	}
	if re.slugs[0] != g.meta.Slug {
		t.Fatalf("replayed slug=%q want %q", re.slugs[0], g.meta.Slug)
	}
}
