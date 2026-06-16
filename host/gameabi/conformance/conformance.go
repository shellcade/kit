package conformance

import (
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/memsvc"
	"github.com/shellcade/kit/v2/host/sdk"
)

// Report is the outcome of a conformance run: the artifact's declared ABI/meta,
// a metric row per executed step, the peak guest linear memory, and the budget
// verdicts. Pass reports true only when every verdict passed.
type Report struct {
	ABIVersion uint32
	Meta       sdk.GameMeta

	Steps    []StepMetric
	PeakMem  uint64 // bytes, peak guest linear memory sampled after each callback
	MemCap   uint64 // bytes, the load-time memory cap
	Deadline time.Duration

	Verdicts []Verdict

	// HibernationChecked is true if the script contained a snapshot/restore
	// checkpoint and the determinism comparison ran; HibernationOK is its result.
	HibernationChecked bool
	HibernationOK      bool
}

// Pass reports whether every verdict passed.
func (r Report) Pass() bool {
	for _, v := range r.Verdicts {
		if !v.OK {
			return false
		}
	}
	return true
}

// StepMetric is one executed step's measurements.
type StepMetric struct {
	Index    int
	Desc     string        // human-readable ("input rune 'p' -> seat 0")
	Callback string        // the ABI callback this step drove ("input", "wake", …) or "" for non-callbacks
	Latency  time.Duration // wall time of the driven callback
	Exit     uint32        // wasm exit code (0 = clean)
	Faulted  bool          // the callback trapped or hit the deadline
	Frames   int           // frames the guest pushed during this step
	MemBytes uint32        // guest linear memory sampled after the step
}

// Verdict is a named budget check. On a breach, Limit/Measured/Step name the
// rule, the offending value, and the step index that breached it.
type Verdict struct {
	Name     string // "callback deadline", "linear memory", "guest fault"
	OK       bool
	Limit    string // the limit, formatted ("100ms", "5 MiB")
	Measured string // the measured value that breached, formatted
	Step     int    // breaching step index (-1 if not step-specific)
	Detail   string
}

// Run executes the script against the artifact at path through the real adapter
// with the given Options (limits ON) and returns a Report. An error is returned
// only for setup failures (load/instantiate); per-step budget breaches are
// reported as failing Verdicts, not errors.
func Run(path string, opts gameabi.Options, script Script) (Report, error) {
	game, err := gameabi.LoadGame(path, opts)
	if err != nil {
		return Report{}, fmt.Errorf("conformance: load: %w", err)
	}
	meta := game.Meta()

	rep := Report{
		ABIVersion: gameabi.Version,
		Meta:       meta,
	}
	if cap, ok := gameabi.MemoryCapBytes(game); ok {
		rep.MemCap = cap
	}

	// Control run: the whole script, no checkpoint interruption. Captures the
	// per-step metrics + peak memory, and (if the script has a checkpoint) the
	// post-checkpoint frames the hibernation comparison expects.
	ctrl := newRun(game, meta)
	ctrlFrames, checkpointAt := ctrl.execute(script, -1) // -1: never snapshot, just record
	rep.Steps = ctrl.metrics
	rep.PeakMem = ctrl.peakMem
	rep.Deadline = ctrl.deadline

	// Hibernation determinism: if the script has a checkpoint, re-run it with a
	// real snapshot/restore at the checkpoint and compare the post-checkpoint
	// frames to the control's.
	var hibDetail string
	if checkpointAt >= 0 {
		rep.HibernationChecked = true
		hib := newRun(game, meta)
		hibFrames, _ := hib.execute(script, checkpointAt)
		// v2 resync (D6): a rejected delta is retried as a keyframe within the
		// same send, so the restored stream is frame-for-frame identical to the
		// control — NO dropped-frame tolerance (kit >= v2.0.1; ABI.md §4.6).
		rep.HibernationOK = framesEqual(ctrlFrames, hibFrames)
		if !rep.HibernationOK {
			hibDetail = diffDetail(ctrlFrames, hibFrames)
		}
	}

	rep.Verdicts = ctrl.verdicts(rep)
	if rep.HibernationChecked {
		v := Verdict{
			Name: "hibernation determinism", OK: rep.HibernationOK, Step: -1,
			Detail: "post-checkpoint frames must match an uninterrupted control",
		}
		if !rep.HibernationOK {
			// Name what actually diverged so an author can find the offending
			// idiom (host-derived state, map-iteration order, an unsnapshotted
			// global) instead of staring at an opaque PASS/FAIL.
			v.Detail = hibDetail
		}
		rep.Verdicts = append(rep.Verdicts, v)
	}
	return rep, nil
}

// ---- the instrumented run ----------------------------------------------------

type run struct {
	game     sdk.Game
	meta     sdk.GameMeta
	cfg      sdk.RoomConfig
	factory  sdk.ServicesFactory
	svc      sdk.Services
	handler  sdk.Handler
	room     *instRoom
	deadline time.Duration
	peakMem  uint64
	metrics  []StepMetric
}

func newRun(game sdk.Game, meta sdk.GameMeta) *run {
	cfg := sdk.RoomConfig{
		Mode:       sdk.ModePrivate,
		Capacity:   max(meta.MaxPlayers, 1),
		MinPlayers: max(meta.MinPlayers, 1),
		Seed:       42,
		SeedSet:    true,
	}
	factory := memsvc.New()
	svc := factory.For("conformance", meta.Slug)
	h := game.NewRoom(cfg, svc)
	r := &instRoom{cfg: cfg, svc: svc, clock: time.Unix(1_700_000_000, 0), log: quietLog()}
	d, _ := gameabi.HandlerDeadline(h)
	return &run{game: game, meta: meta, cfg: cfg, factory: factory, svc: svc, handler: h, room: r, deadline: d}
}

// execute drives the script. If snapAt >= 0, a real snapshot/restore is
// performed at the step with that index (which must be a SnapshotRestore step);
// otherwise SnapshotRestore steps are no-ops (the control path). It returns the
// frames pushed AFTER the checkpoint (for the determinism comparison) and the
// index of the checkpoint step (-1 if none).
func (rn *run) execute(script Script, snapAt int) (postFrames []sdk.Frame, checkpointAt int) {
	checkpointAt = -1
	rn.handler.OnStart(rn.room)
	rn.sample("start", "start", 0)

	postCheckpoint := false
	for i, step := range script {
		switch step.Kind {
		case StepSnapshotRestore:
			checkpointAt = i
			postCheckpoint = true
			if snapAt == i {
				rn.doSnapshotRestore(i)
			}
			rn.metrics = append(rn.metrics, StepMetric{Index: i, Desc: "snapshot/restore checkpoint", MemBytes: gameabi.GuestMemorySize(rn.handler)})
		case StepAdvance:
			rn.room.clock = rn.room.clock.Add(time.Duration(step.Dur) * time.Millisecond)
			rn.metrics = append(rn.metrics, StepMetric{Index: i, Desc: fmt.Sprintf("advance clock %dms", step.Dur), MemBytes: gameabi.GuestMemorySize(rn.handler)})
		case StepJoin:
			p := rn.room.seatPlayer(step.Seat)
			rn.room.join(p)
			rn.drive(i, fmt.Sprintf("join seat %d (%s)", step.Seat, p.Handle), "join", func() { rn.handler.OnJoin(rn.room, p) })
		case StepLeave:
			p := rn.room.seatPlayer(step.Seat)
			rn.room.leave(p)
			rn.drive(i, fmt.Sprintf("leave seat %d (%s)", step.Seat, p.Handle), "leave", func() { rn.handler.OnLeave(rn.room, p) })
		case StepInput:
			p := rn.room.seatPlayer(step.Seat)
			in := inputFor(step)
			rn.drive(i, fmt.Sprintf("input %s -> seat %d", describeInput(step), step.Seat), "input", func() { rn.handler.OnInput(rn.room, p, in) })
		case StepWake:
			rn.drive(i, "wake", "wake", func() { rn.handler.OnTick(rn.room, rn.room.clock) })
		}
		if postCheckpoint {
			postFrames = append(postFrames, rn.room.takeFrames()...)
		} else {
			rn.room.takeFrames() // discard pre-checkpoint frames
		}
	}
	return postFrames, checkpointAt
}

// drive runs one callback, timing it, then records the per-step metric and
// samples guest memory.
func (rn *run) drive(idx int, desc, callback string, call func()) {
	rn.room.frameMark = len(rn.room.frames)
	start := time.Now()
	call()
	lat := time.Since(start)
	exit, _, faulted := gameabi.LastCallback(rn.handler)
	rn.recordStep(idx, desc, callback, lat, exit, faulted)
}

func (rn *run) recordStep(idx int, desc, callback string, lat time.Duration, exit uint32, faulted bool) {
	mem := gameabi.GuestMemorySize(rn.handler)
	if uint64(mem) > rn.peakMem {
		rn.peakMem = uint64(mem)
	}
	rn.metrics = append(rn.metrics, StepMetric{
		Index: idx, Desc: desc, Callback: callback, Latency: lat,
		Exit: exit, Faulted: faulted,
		Frames:   len(rn.room.frames) - rn.room.frameMark,
		MemBytes: mem,
	})
}

// sample records a non-step callback (OnStart) and updates peak memory.
func (rn *run) sample(desc, callback string, idx int) {
	mem := gameabi.GuestMemorySize(rn.handler)
	if uint64(mem) > rn.peakMem {
		rn.peakMem = uint64(mem)
	}
	exit, _, faulted := gameabi.LastCallback(rn.handler)
	rn.metrics = append(rn.metrics, StepMetric{Index: -1, Desc: desc, Callback: callback, Exit: exit, Faulted: faulted, MemBytes: mem})
	rn.room.takeFrames()
}

// doSnapshotRestore snapshots the live handler and replaces it with a restored
// one bound to the same game, continuing from the restored state.
func (rn *run) doSnapshotRestore(idx int) {
	blob, err := gameabi.SnapshotHandler(rn.handler)
	if err != nil {
		rn.room.log.Error("conformance: snapshot failed", "step", idx, "err", err)
		return
	}
	h, err := gameabi.RestoreHandler(rn.game, blob)
	if err != nil {
		rn.room.log.Error("conformance: restore failed", "step", idx, "err", err)
		return
	}
	// Rebind the room's live services onto the restored handler: services are
	// host resources the snapshot does not carry, so a resumed room must be
	// rewired to the running instance's services — exactly as the engine will
	// when it restores a hibernated room. Without this, kv/config/leaderboard
	// host calls no-op after the checkpoint and the resumed room diverges from
	// the uninterrupted control.
	gameabi.BindServices(h, rn.svc)
	// The restored handler supersedes the live one — close the replaced
	// instance so its linear memory is not pinned in the game's shared wazero
	// runtime for the rest of the harness run (the unadopted-handler rule).
	gameabi.CloseHandler(rn.handler)
	rn.handler = h

	// Drive the engine's resume entry point (OnResume) on the restored handler,
	// exactly as the lobby resume flow does. In ABI v2 this re-seeds the host's
	// ephemeral frame-delta epoch above the snapshot high-water and marks every
	// per-consumer baseline slot not-present (D6), so the restored guest's first
	// post-restore send is epoch-rejected and self-heals to a keyframe.
	if rsm, ok := rn.handler.(sdk.Resumed); ok {
		rsm.OnResume(rn.room)
		rn.room.takeFrames() // the resume callback itself renders no compared frame
	}
}

// verdicts derives the named budget checks from the recorded metrics.
func (rn *run) verdicts(rep Report) []Verdict {
	var vs []Verdict

	// Guest-fault verdict: any callback that trapped or hit the deadline.
	faultStep, faulted := -1, false
	for _, m := range rn.metrics {
		if m.Faulted {
			faultStep, faulted = m.Index, true
			break
		}
	}
	if faulted {
		// Distinguish a deadline kill (latency at/over the deadline) from a trap.
		name, limit, measured := "guest fault", "exit 0", "non-zero exit/trap"
		for _, m := range rn.metrics {
			if m.Index == faultStep && m.Faulted {
				if rn.deadline > 0 && m.Latency >= rn.deadline {
					name = "callback deadline"
					limit = rn.deadline.String()
					measured = m.Latency.Round(time.Millisecond).String()
				} else {
					measured = fmt.Sprintf("exit %d", m.Exit)
				}
				break
			}
		}
		vs = append(vs, Verdict{Name: name, OK: false, Limit: limit, Measured: measured, Step: faultStep,
			Detail: "a guest callback faulted; the room was force-settled"})
	} else {
		vs = append(vs, Verdict{Name: "guest fault", OK: true, Limit: "none", Measured: "no fault", Step: -1})
	}

	// Deadline verdict (even without a fault, a callback may run long): the worst
	// latency must stay under the per-callback deadline.
	if rn.deadline > 0 {
		worst, worstStep := time.Duration(0), -1
		for _, m := range rn.metrics {
			if m.Latency > worst {
				worst, worstStep = m.Latency, m.Index
			}
		}
		ok := worst < rn.deadline
		v := Verdict{Name: "callback latency", OK: ok, Limit: rn.deadline.String(),
			Measured: worst.Round(time.Microsecond).String(), Step: worstStep}
		if !ok {
			v.Detail = "a callback's wall time reached the per-callback deadline"
		}
		vs = append(vs, v)
	}

	// Memory verdict: peak guest memory under the cap.
	if rep.MemCap > 0 {
		ok := rep.PeakMem < rep.MemCap
		v := Verdict{Name: "linear memory", OK: ok, Limit: humanBytes(rep.MemCap),
			Measured: humanBytes(rep.PeakMem), Step: -1}
		if !ok {
			v.Detail = "peak guest linear memory reached the manifest cap"
		}
		vs = append(vs, v)
	}

	return vs
}

// LeaderboardVerdict is the publishing-policy gate that every PUBLISHED game must
// declare a leaderboard so its results are recorded and ranked. It is NOT part of
// the generic ABI conformance verdicts (which also run against minimal test
// fixtures that legitimately declare no board): callers that enforce the catalog
// publishing policy — `shellcade-kit check --require-leaderboard`, used by the
// games-repo CI — append it to the Report before checking Pass().
//
// It is deliberately STATIC (a check on GameMeta), not a behavioral "posts on
// leave" assertion. A mid-play disconnect in a round-based multiplayer game is
// recorded at round SETTLEMENT (the leaver ranked dnf), not necessarily on the
// leave callback, and the remaining player may keep playing — so a behavioral
// gate false-fails correct games. The disconnect-save and continuous
// periodic-save behavior is specified in game-sdk and verified by each game's
// own tests.
func LeaderboardVerdict(meta sdk.GameMeta) Verdict {
	declared := meta.Leaderboard != nil
	v := Verdict{Name: "leaderboard declared", OK: declared, Limit: "Meta().Leaderboard set", Step: -1}
	if declared {
		v.Measured = "declared"
	} else {
		v.Measured = "missing"
		v.Detail = fmt.Sprintf("game %q declares no leaderboard; every published game must declare a LeaderboardSpec "+
			"in GameMeta so its results are recorded and ranked", meta.Slug)
	}
	return v
}

// ---- helpers -----------------------------------------------------------------

func inputFor(s Step) sdk.Input {
	if s.Rune != 0 {
		return sdk.Input{Kind: sdk.InputRune, Rune: s.Rune}
	}
	return sdk.Input{Kind: sdk.InputKey, Key: sdk.Key(s.Key)}
}

func describeInput(s Step) string {
	if s.Rune != 0 {
		return fmt.Sprintf("rune %q", s.Rune)
	}
	return fmt.Sprintf("key %d", s.Key)
}

func framesEqual(a, b []sdk.Frame) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Cells != b[i].Cells {
			return false
		}
	}
	return true
}

// (framesEqualAfterResync removed.) Under the ABI v2 hibernation contract the
// restored guest's first send per consumer slot is epoch-rejected — and the
// guest retries the same frame as a keyframe within the same call (ABI.md
// §4.6, kit >= v2.0.1), so the restored stream must match the control
// frame-for-frame (framesEqual) with no dropped-frame tolerance.

// diffDetail pinpoints the FIRST way the restored frame stream diverged from the
// control's, so the failure message teaches the author what to fix rather than
// just reporting PASS/FAIL. It names a frame-count mismatch, or the first frame
// and the first cell (row/col) whose rune differs, with both sides rendered.
func diffDetail(ctrl, hib []sdk.Frame) string {
	if len(ctrl) != len(hib) {
		return fmt.Sprintf("restored room pushed %d post-checkpoint frame(s); the control pushed %d "+
			"(a callback took a different path after restore — likely host-derived state the snapshot does not carry)",
			len(hib), len(ctrl))
	}
	for fi := range ctrl {
		if ctrl[fi].Cells == hib[fi].Cells {
			continue
		}
		// Prefer a rune divergence (the loudest, most actionable signal); fall
		// back to naming a style-only divergence at the first differing cell.
		styleRow, styleCol := -1, -1
		for row := 0; row < canvasRows; row++ {
			for col := 0; col < canvasCols; col++ {
				cc, hc := ctrl[fi].Cells[row][col], hib[fi].Cells[row][col]
				if cc == hc {
					continue
				}
				if cc.Rune != hc.Rune {
					return fmt.Sprintf("post-checkpoint frame %d diverged at cell (row %d, col %d): "+
						"control rune %q, restored rune %q — the resumed guest read state the snapshot did not "+
						"reproduce (e.g. a host-time/wall-offset animation, map-iteration order, or an unsnapshotted runtime global)",
						fi, row, col, runeOrDot(cc.Rune), runeOrDot(hc.Rune))
				}
				if styleRow < 0 {
					styleRow, styleCol = row, col
				}
			}
		}
		// No rune differed in this frame, but its Cells array did — style-only.
		return fmt.Sprintf("post-checkpoint frame %d diverged in cell styling at (row %d, col %d) "+
			"though every rune matched — the resumed guest produced a different color/attribute",
			fi, styleRow, styleCol)
	}
	return "post-checkpoint frames differ (no specific cell located)"
}

const (
	canvasRows = 24
	canvasCols = 80
)

func runeOrDot(r rune) string {
	if r == 0 || r == ' ' {
		return "·"
	}
	return string(r)
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%d MiB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KiB", b/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// ---- instrumented Room -------------------------------------------------------

// instRoom is the synchronous Room the harness drives the handler against: a
// seat-indexed roster, a virtual clock the harness advances, and a frame log it
// samples per step. It mirrors the engine's roster/clock/effect surface without
// a goroutine, so the harness can measure each callback in isolation.
type instRoom struct {
	cfg   sdk.RoomConfig
	svc   sdk.Services
	clock time.Time
	log   *slog.Logger

	members   []sdk.Player
	frames    []sdk.Frame
	frameMark int
	last      map[string]sdk.Frame // accountID -> latest frame (for RunShots)

	ended bool
	res   sdk.Result
	ctx   sdk.InputContext
}

// seatPlayer returns a stable Player for a seat index (deterministic identity so
// rejoin resolves to the same account). Conformance validates ABI/limits/
// determinism/post-on-leave, not character rendering, so it uses a zero
// character rather than projecting a default one.
func (r *instRoom) seatPlayer(seat int) sdk.Player {
	return sdk.Player{
		AccountID: fmt.Sprintf("seat-%d", seat),
		Handle:    fmt.Sprintf("seat%d", seat),
		Kind:      sdk.KindMember,
		Conn:      fmt.Sprintf("conn-%d", seat),
	}
}

func (r *instRoom) join(p sdk.Player) {
	for _, m := range r.members {
		if m == p {
			return
		}
	}
	r.members = append(r.members, p)
}

func (r *instRoom) leave(p sdk.Player) {
	out := r.members[:0]
	for _, m := range r.members {
		if m != p {
			out = append(out, m)
		}
	}
	r.members = out
}

func (r *instRoom) takeFrames() []sdk.Frame {
	f := r.frames
	r.frames = nil
	return f
}

// ---- sdk.Room implementation ----

func (r *instRoom) Members() []sdk.Player { return append([]sdk.Player(nil), r.members...) }
func (r *instRoom) Has(p sdk.Player) bool {
	for _, m := range r.members {
		if m == p {
			return true
		}
	}
	return false
}
func (r *instRoom) Count() int             { return len(r.members) }
func (r *instRoom) Config() sdk.RoomConfig { return r.cfg }
func (r *instRoom) Rand() *rand.Rand       { return rand.New(rand.NewSource(r.cfg.Seed)) }
func (r *instRoom) Now() time.Time         { return r.clock }

func (r *instRoom) Send(p sdk.Player, f sdk.Frame) {
	r.frames = append(r.frames, f)
	r.remember(p, f)
}

func (r *instRoom) Identical(f sdk.Frame) {
	r.frames = append(r.frames, f)
	for _, p := range r.members {
		r.remember(p, f)
	}
}

func (r *instRoom) BroadcastFunc(compose func(p sdk.Player) sdk.Frame) {
	for _, p := range r.members {
		f := compose(p)
		r.frames = append(r.frames, f)
		r.remember(p, f)
	}
}

// remember keeps the latest frame per seat for RunShots' capture markers.
// sdk.Frame is a value (the canvas grid array), so the map entry is a copy.
func (r *instRoom) remember(p sdk.Player, f sdk.Frame) {
	if r.last == nil {
		r.last = map[string]sdk.Frame{}
	}
	r.last[p.AccountID] = f
}

func (r *instRoom) After(time.Duration, func(sdk.Room)) sdk.TimerID { return 0 }
func (r *instRoom) Every(time.Duration, func(sdk.Room)) sdk.TimerID { return 0 }
func (r *instRoom) Cancel(sdk.TimerID)                              {}
func (r *instRoom) SetSimRate(time.Duration)                        {}
func (r *instRoom) SetFrameRate(time.Duration)                      {}
func (r *instRoom) SetPhase(string, bool, time.Time)                {}
func (r *instRoom) SetInputContext(ctx sdk.InputContext)            { r.ctx = ctx }

func (r *instRoom) End(res sdk.Result) {
	if r.ended {
		return
	}
	r.ended, r.res = true, res
}
func (r *instRoom) Result() (sdk.Result, bool) {
	if r.ended {
		return r.res, true
	}
	return sdk.Result{}, false
}
func (r *instRoom) Services() sdk.Services { return r.svc }
func (r *instRoom) Log() *slog.Logger      { return r.log }

var _ sdk.Room = (*instRoom)(nil)
