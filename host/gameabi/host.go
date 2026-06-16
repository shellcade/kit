package gameabi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	extism "github.com/extism/go-sdk"
	"github.com/tetratelabs/wazero"
	wzsys "github.com/tetratelabs/wazero/sys"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/sdk"
)

// kvTimeout caps a kv/config host call's store context. The context is DERIVED
// from the callback context (see the kv host functions), so the effective bound
// is min(remaining callback deadline, kvTimeout) — a slow Postgres can never
// hold the room actor past the per-callback kill switch.
const kvTimeout = 2 * time.Second

// Guest log caps: log volume is the one resource the wasm sandbox (memory cap,
// CPU deadline, virtualized WASI) does not otherwise meter, so a printf-spamming
// guest could inflate log costs and drown the audit/quarantine events operators
// need. Both guest log paths — stdout/stderr (logWriter) and the `log` host
// function — share one per-room byte budget per real-time window, with each
// write truncated. One Warn marker per window records that limiting happened.
const (
	guestLogMaxWrite = 4 << 10  // bytes per write/log call; longer payloads are truncated
	guestLogBudget   = 32 << 10 // bytes per room per window, across both paths
	guestLogWindow   = time.Second
)

// DefaultCallbackDeadline is the per-callback wall-clock kill switch when
// Options.CallbackDeadline is unset (admin-tunable per game via
// host.deadline_ms — see HostConfigSpecs).
const DefaultCallbackDeadline = 100 * time.Millisecond

// Options bound a loaded game's runtime behavior. Zero values take defaults.
type Options struct {
	Heartbeat        time.Duration // wake cadence (default Heartbeat)
	MemoryPages      uint32        // linear-memory cap in 64KiB pages (default 512 = 32MiB)
	CallbackDeadline time.Duration // per-callback wall-clock kill switch (default 100ms)

	// OnFault, when set, is told the game's slug each time a guest faults
	// (failed instantiation, trap, callback deadline, memory cap). Wire it to
	// Quarantine.RecordFault to remove repeat offenders from the live roster.
	// Called from room actor goroutines; must be safe for concurrent use.
	OnFault func(slug string)

	// Metrics, when set, receives host-measured per-game counters (add-metrics).
	// Every value is measured by the HOST from bytes it moved across the module
	// boundary — never a module-reported figure, so a guest cannot inflate or
	// fabricate a count. Called from room actor goroutines; implementations must
	// be safe for concurrent use. nil ⇒ no recording.
	Metrics Metrics
}

// Metrics is the host-side per-game instrumentation surface (implemented by
// *metrics.Metrics; defined here so gameabi does not import the metrics
// package). Byte counts are logical (pre-terminal-encoding) frame/input sizes
// — the per-game attribution numbers, not wire bytes.
type Metrics interface {
	GameFrameBytesOut(slug string, n int)
	GameInputBytesIn(slug string, n int)
	GameFault(slug string)
	// GameCallback records the host-measured wall-clock duration of one guest
	// callback (the CPU-attribution surface: a spinning game piles into the top
	// bucket right before its deadline kill).
	GameCallback(slug, callback string, seconds float64)
	// GameCallbackDeadline records one callback the kill switch fired on — a
	// spin-to-deadline, distinct from other faults.
	GameCallbackDeadline(slug, callback string)
	// GameHostIODeadline records one callback the kill switch fired on while
	// the guest was blocked in the host's OWN store/config call — a host-I/O
	// incident (slow shared Postgres), deliberately excluded from the fault
	// path so DB slowness never feeds quarantine.
	GameHostIODeadline(slug, callback string)
	// GameKVError records one failed kv/config host call (op: kv_get | kv_set |
	// kv_delete | config_get) — silent dropped writes made visible.
	GameKVError(slug, op string)
	// GameLinearMemoryDelta adjusts the per-game linear-memory gauge by delta
	// bytes. The host samples each room's ACTUAL guest memory size (wazero
	// Memory.Size — never a module-reported figure) on the room heartbeat and
	// reports the change since the room's previous sample, retiring the room's
	// whole contribution when its instance closes, so the gauge is the sum
	// across the game's live rooms with no per-room series.
	GameLinearMemoryDelta(slug string, delta int64)
}

func (o Options) withDefaults() Options {
	if o.Heartbeat <= 0 {
		o.Heartbeat = Heartbeat
	}
	if o.MemoryPages == 0 {
		o.MemoryPages = 512 // 32 MiB
	}
	if o.CallbackDeadline <= 0 {
		o.CallbackDeadline = DefaultCallbackDeadline
	}
	return o
}

// handlerKey carries the current room's wasmHandler through the Call context so
// host functions resolve against the callback's roster (mid-callback-only rule).
type handlerKey struct{}

// LoadGame compiles a wasm artifact from a file, validates the ABI handshake and
// meta on a throwaway instance, and returns a sdk.Game whose rooms run the guest.
func LoadGame(path string, opts Options) (sdk.Game, error) {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadGameBytes(wasm, opts)
}

// LoadGameBytes is LoadGame over an in-memory artifact (the catalog loads a
// verified blob from the object store, never a path). The two share all
// compile/handshake/meta logic; LoadGame is the file-backed convenience.
func LoadGameBytes(wasm []byte, opts Options) (sdk.Game, error) {
	opts = opts.withDefaults()
	g := &wasmGame{wasm: wasm, wasmSHA: sha256.Sum256(wasm), opts: opts}

	// Limits (D6): linear-memory cap via the manifest; the per-callback
	// deadline needs the runtime to honor context cancellation.
	manifest := extism.Manifest{
		Wasm:   []extism.Wasm{extism.WasmData{Data: wasm}},
		Memory: &extism.ManifestMemory{MaxPages: opts.MemoryPages},
	}
	cfg := extism.PluginConfig{
		EnableWasi:    true,
		RuntimeConfig: wazero.NewRuntimeConfig().WithCloseOnContextDone(true),
	}
	compiled, err := extism.NewCompiledPlugin(context.Background(), manifest, cfg, hostFunctions())
	if err != nil {
		return nil, fmt.Errorf("gameabi: compile: %w", err)
	}
	g.compiled = compiled

	// Throwaway instance: handshake + meta, then discard.
	probe, err := compiled.Instance(context.Background(), extism.PluginInstanceConfig{})
	if err != nil {
		return nil, fmt.Errorf("gameabi: instantiate probe: %w", err)
	}
	defer probe.Close(context.Background())
	exit, out, err := probe.Call(wire.ExpABI, nil)
	if err != nil || exit != 0 {
		return nil, fmt.Errorf("gameabi: %s failed (exit %d): %v", wire.ExpABI, exit, err)
	}
	if len(out) < 4 {
		return nil, fmt.Errorf("gameabi: %s returned %d bytes, want 4", wire.ExpABI, len(out))
	}
	v := uint32(out[0]) | uint32(out[1])<<8 | uint32(out[2])<<16 | uint32(out[3])<<24
	// v2 is a hard major cutover (D-transition): the host requires major 2 and
	// refuses any other major — there is no dual loader and no v1 ingestion path.
	// A major-1 artifact (the pre-diffing 16-byte-cell encoding) is unsupported.
	if v != Version {
		return nil, fmt.Errorf("gameabi: unsupported ABI major v%d (host requires v%d); refusing to instantiate", v, Version)
	}
	g.abiMajor = v
	exit, out, err = probe.Call(wire.ExpMeta, nil)
	if err != nil || exit != 0 {
		return nil, fmt.Errorf("gameabi: %s failed (exit %d): %v", wire.ExpMeta, exit, err)
	}
	meta, err := decodeMeta(out)
	if err != nil {
		return nil, err
	}
	if err := validateBareName(meta.Slug); err != nil {
		return nil, err
	}
	g.meta = meta
	return g, nil
}

// bareName matches the slug a guest is allowed to declare: a lower-case
// kebab-case identifier with NO namespace separator. A game's full platform
// slug is <shellcade-username>/<name>; the namespace prefix is composed
// host-side from the verified submitter, so a binary can never claim one — it
// names ONLY the bare game name here.
var bareName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// validateBareName rejects a guest meta whose declared slug is not a bare name
// (e.g. carries a "/" namespace, an upper-case letter, or is over-long). The
// host owns namespacing, so a binary that tries to ship a namespaced slug is a
// malformed artifact, not a loadable game.
func validateBareName(slug string) error {
	if !bareName.MatchString(slug) {
		return fmt.Errorf("gameabi: guest declared slug %q; meta.slug must be a bare name matching %s (the host composes the <author>/<name> namespace)", slug, bareName.String())
	}
	return nil
}

// wasmGame implements sdk.Game over a compiled Extism plugin.
type wasmGame struct {
	sdk.GameBase
	wasm     []byte
	wasmSHA  [sha256.Size]byte // artifact hash, bound into snapshots (restore must match)
	compiled *extism.CompiledPlugin
	meta     sdk.GameMeta
	opts     Options
	abiMajor uint32 // the major the guest declared at the handshake probe
}

// OverrideSlug renames a loaded wasm game (dev sideloading: avoid colliding
// with a compiled-in slug). Returns false if g is not a wasm game.
func OverrideSlug(g sdk.Game, slug string) bool {
	wg, ok := g.(*wasmGame)
	if !ok {
		return false
	}
	wg.meta.Slug = slug
	return true
}

// SetHidden marks a loaded wasm game live-but-unlisted (sdk.GameMeta.Hidden): it
// stays reachable by exact slug (quick-match, direct entry, admin) but the
// lobby's player-facing menu omits it. Used for the built-in load-test game
// (add-loadtest-harness). ok is false if g is not a wasm game.
func SetHidden(g sdk.Game, hidden bool) bool {
	wg, ok := g.(*wasmGame)
	if !ok {
		return false
	}
	wg.meta.Hidden = hidden
	return true
}

// SetMaxPlayers overrides the room capacity a loaded wasm game declares. The
// built-in load-test game uses this to cap lobby size (a guest may declare a huge
// MaxPlayers as a stress testbed; the host bounds it). ok is false if g is not a
// wasm game.
func SetMaxPlayers(g sdk.Game, n int) bool {
	wg, ok := g.(*wasmGame)
	if !ok {
		return false
	}
	wg.meta.MaxPlayers = n
	return true
}

// SetLifecycle overrides the room end-of-life mode a loaded wasm game declares.
// The built-in load-test game forces Ephemeral so its rooms run while players are
// connected and dispose after the abandon grace — never hibernated (no parked-room
// snapshots) and never resident (no always-on idle tick). ok is false if g is not
// a wasm game.
func SetLifecycle(g sdk.Game, lc sdk.Lifecycle) bool {
	wg, ok := g.(*wasmGame)
	if !ok {
		return false
	}
	wg.meta.Lifecycle = lc
	return true
}

// CallbackDeadlineOf reports the per-callback wall-clock deadline a loaded wasm
// game runs under. ok is false if g is not a wasm game. Inspection helper (the
// load-test game runs a generous deadline so heavy ticks degrade, not trap).
func CallbackDeadlineOf(g sdk.Game) (time.Duration, bool) {
	wg, ok := g.(*wasmGame)
	if !ok {
		return 0, false
	}
	return wg.opts.CallbackDeadline, true
}

// QuarantineExempt reports whether a loaded wasm game has NO fault hook wired, so
// the fault watchdog can never quarantine it — as the built-in load-test game is
// (it must survive the overload it exists to measure). ok is false if g is not a
// wasm game.
func QuarantineExempt(g sdk.Game) (exempt, ok bool) {
	wg, isWasm := g.(*wasmGame)
	if !isWasm {
		return false, false
	}
	return wg.opts.OnFault == nil, true
}

// ABIMajor reports the ABI major a loaded wasm game declared at the handshake
// probe (record-abi-major); ok is false if g is not a wasm game. Today the
// value always equals Version — LoadGameBytes refuses any other major — but
// the catalog persists the PROBED value per verified version so the column
// stays truthful the day the host accepts a set of majors.
func ABIMajor(g sdk.Game) (major uint32, ok bool) {
	wg, ok := g.(*wasmGame)
	if !ok {
		return 0, false
	}
	return wg.abiMajor, true
}

func (g *wasmGame) Meta() sdk.GameMeta { return g.meta }

// Reserved host config keys (D6): an admin tunes a deployed game's runtime
// cadence and per-callback kill switch per slug without a rebuild. Values are
// base-10 milliseconds; invalid or out-of-range values fall back to the loaded
// Options. MemoryPages is NOT here — the linear-memory cap is fixed in the
// compiled manifest (LoadGame), so it cannot change per room; admins must
// reload the game to change it.
const (
	cfgHeartbeatMS = "host.heartbeat_ms"
	cfgDeadlineMS  = "host.deadline_ms"
)

// Sane bounds for the config-driven knobs (a hostile or fat-fingered config
// value can't starve the actor or hand a runaway guest an unbounded budget).
const (
	minHeartbeat = 20 * time.Millisecond
	maxHeartbeat = 1 * time.Second
	minDeadline  = 10 * time.Millisecond
	maxDeadline  = 2 * time.Second
)

// ResidentConfigKey is the reserved admin config key granting the resident
// lifecycle to a slug (room-lifecycle-modes): a bool, set via the admin Game
// settings UI, read by the matchmaker when resolving a game's lifecycle.
const ResidentConfigKey = "host.resident"

// HostConfigSpecs declares the reserved host-interpreted config keys as
// platform-side specs (add-config-specs), so the admin Game settings area
// renders them through the same typed machinery as game-declared keys — for
// every wasm game, replacing hand-written help text. Defaults and bounds are
// sourced from the constants above; like any config write, a change applies
// to NEW rooms only (read once at room construction).
func HostConfigSpecs() []sdk.ConfigKeySpec {
	return []sdk.ConfigKeySpec{
		{
			Key:   cfgHeartbeatMS,
			Title: "Host heartbeat",
			Description: fmt.Sprintf(
				"Wake cadence in milliseconds — the host tick budget. Clamped to %d–%d ms; applies to new rooms.",
				minHeartbeat.Milliseconds(), maxHeartbeat.Milliseconds()),
			Type:    sdk.ConfigNumber,
			Default: strconv.FormatInt(Heartbeat.Milliseconds(), 10),
		},
		{
			Key:   ResidentConfigKey,
			Title: "Resident world",
			Description: "Grant the resident lifecycle: one always-on world for this game " +
				"(boot-restored, ticks while empty, never in the Resume menu). Takes effect " +
				"only for games DECLARING the resident lifecycle in their meta; granting is " +
				"an operator decision — applies within ~15s to new joins.",
			Type:    sdk.ConfigBool,
			Default: "false",
		},
		{
			Key:   cfgDeadlineMS,
			Title: "Callback deadline",
			Description: fmt.Sprintf(
				"Per-callback wall-clock kill switch in milliseconds. Clamped to %d–%d ms; applies to new rooms.",
				minDeadline.Milliseconds(), maxDeadline.Milliseconds()),
			Type:    sdk.ConfigNumber,
			Default: strconv.FormatInt(DefaultCallbackDeadline.Milliseconds(), 10),
		},
	}
}

func (g *wasmGame) NewRoom(cfg sdk.RoomConfig, svc sdk.Services) sdk.Handler {
	h := &wasmHandler{
		game:      g,
		cfg:       cfg,
		svc:       svc,
		heartbeat: g.opts.Heartbeat,
		deadline:  g.opts.CallbackDeadline,
		epochMode: g.meta.CtxFeatures&wire.CtxFeatRosterEpoch != 0,
	}
	h.forceFullRoster = h.epochMode
	// Heartbeat precedence: admin host.heartbeat_ms config > the game's
	// declared HeartbeatMS > the loaded default — declaration applied first
	// so the admin override below still wins. Always clamped to the envelope.
	if g.meta.HeartbeatMS > 0 {
		h.heartbeat = clampDur(time.Duration(g.meta.HeartbeatMS)*time.Millisecond, minHeartbeat, maxHeartbeat)
	}
	// Per-room config overrides (admin-tunable; applies to NEW rooms only, since
	// it is read once here at room construction). The ConfigStore is slug-bound,
	// so a game can only see its own host.* keys; it may be nil (no config).
	if svc.Config != nil {
		if d, ok := readConfigDuration(svc.Config, cfgHeartbeatMS); ok {
			h.heartbeat = clampDur(d, minHeartbeat, maxHeartbeat)
		}
		if d, ok := readConfigDuration(svc.Config, cfgDeadlineMS); ok {
			h.deadline = clampDur(d, minDeadline, maxDeadline)
		}
	}
	return h
}

// readConfigDuration reads a reserved host key as base-10 milliseconds. A
// missing, malformed, or non-positive value reads as "no override" so the
// loaded Options stand.
func readConfigDuration(store sdk.ConfigStore, key string) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), kvTimeout)
	defer cancel()
	v, ok, err := store.Get(ctx, key)
	if err != nil || !ok {
		return 0, false
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
	if err != nil || ms <= 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// wasmHandler implements sdk.Handler for ONE room by delegating to one plugin
// instance. All callbacks run on the room's actor goroutine, so cur/roster
// need no locking; host functions are honored only while cur != nil.
type wasmHandler struct {
	sdk.Base
	game *wasmGame
	cfg  sdk.RoomConfig
	svc  sdk.Services

	// Per-room runtime knobs: seeded from the loaded Options, then overridden by
	// the room's host.* config keys in NewRoom. The memory cap stays load-time
	// (manifest-fixed), so it is not mirrored here.
	heartbeat time.Duration
	deadline  time.Duration

	inst *extism.Plugin

	// Virtualized-entropy bookkeeping (snapshot determinism): the seed the room's
	// rand source was created from, and a counter wrapping that source so a
	// restore can replay the stream to the same position.
	seed    int64
	entropy *countingReader

	// inputCtx mirrors the latest SetInputContext the guest published, so a
	// snapshot can carry the room's current input context.
	inputCtx sdk.InputContext

	// postSeq counts the leaderboard posts this room has issued (1-based; the
	// value stamped on a Result is the post-increment counter). Persisted in
	// the snapshot blob (format 4) so a room reclaimed from a pre-settle
	// checkpoint re-issues the SAME sequence when it re-settles the same round
	// — the durable leaderboard derives the round id deterministically from
	// (roomID, postSeq), making post-restore re-settles (and write retries)
	// idempotent. Actor-goroutine owned.
	postSeq uint32

	// Per-consumer frame-delta baseline+epoch authority (D4/D7/D9). Host memory
	// (not guest linear memory, not snapshotted); actor-goroutine owned like
	// cur/roster, so no locking. ~0.78 MB/room at 24-byte cells.
	baselines baselineCache

	// Roster-epoch mode (the guest's meta declared wire.CtxFeatRosterEpoch):
	// rosterEpoch bumps on every roster mutation; lastFullEpoch is the epoch
	// most recently sent in the 0xFFFE full form to THIS instance (0 = never
	// — both zero on construction AND on restore, forcing the first callback
	// of any instantiation to carry the full roster). Ephemeral host memory,
	// never snapshotted.
	epochMode     bool
	rosterEpoch   uint32
	lastFullEpoch uint32
	// forceFullRoster forces the next callback's member section to the full
	// form regardless of epoch equality — set on construction AND on restore
	// (where rosterFP is deliberately seeded from the snapshot roster, so the
	// fingerprint bump does not fire for a same-roster resume).
	forceFullRoster bool

	// Callback time accounting (actor-goroutine owned, no locking): total
	// wall time inside guest calls, and the portion spent in the send/
	// identical HOST functions the guest invokes mid-callback (delta apply +
	// frame decode + fan-out). guest-pure time = total - host. Cumulative;
	// read via CallbackSplit for benchmarks/diagnostics.
	cbTotalNanos int64
	cbHostNanos  int64

	// hostIOExpired marks that the CURRENT callback's kill-switch context
	// expired while the guest was blocked in the host's own kv/config store
	// call (set by the kv host functions, reset at the top of invoke). Together
	// with the per-callback host-time split it discriminates "the guest spun"
	// from "the host's I/O was slow" in the trap path, so a Postgres brownout
	// is never booked as a game fault feeding quarantine. Actor-goroutine owned.
	hostIOExpired bool

	// Guest log budget bookkeeping (actor-goroutine owned): bytes emitted in
	// the current real-time window across stdout/stderr and the log host
	// function, plus whether the one-per-window rate-limited marker fired.
	logWindowStart time.Time
	logBytes       int
	logLimited     bool

	// rosterFP fingerprints the last callback's roster so a roster mutation
	// (join/leave/index shift) can invalidate every baseline slot (D7/4.6).
	rosterFP uint64

	// memSampled is the guest linear-memory size (bytes) last folded into the
	// per-game memory gauge — sampleMemory reports deltas against it on each
	// heartbeat and closeInstance retires it. Actor-goroutine owned.
	memSampled uint32

	// last callback outcome (for out-of-package instrumentation, e.g. the
	// conformance harness): the wasm exit code and any trap/deadline error.
	lastExit uint32
	lastErr  error

	// callback-scoped (actor goroutine only)
	cur          sdk.Room
	roster       []sdk.Player // the last callback's roster; survives between calls for Snapshot
	dead         bool
	ended        bool  // guest called end() — the room is settling
	closePending bool  // OnClose arrived while a guest call was on the stack
	nowNanos     int64 // room clock for the virtualized WASI surface (actor-owned)
}

func (h *wasmHandler) OnStart(r sdk.Room) {
	h.nowNanos = r.Now().UnixNano()
	h.seed = h.cfg.Seed
	if !h.cfg.SeedSet {
		h.seed = h.nowNanos
	}

	inst, err := h.game.compiled.Instance(context.Background(),
		extism.PluginInstanceConfig{ModuleConfig: h.moduleConfig(h.seed)})
	if err != nil {
		r.Log().Error("gameabi: instantiate room", "err", err)
		h.fault()
		r.End(sdk.Result{Mode: h.cfg.Mode})
		h.dead = true
		return
	}
	h.inst = inst
	r.SetSimRate(h.heartbeat) // engine OnTick drives the guest heartbeat (per-room)
	h.call(r, wire.ExpStart, nil)
	h.sampleMemory() // first gauge sample at birth; the heartbeat keeps it fresh
}

// moduleConfig builds the virtualized WASI surface (D10): the guest's built-in
// clock reads the room clock (== CallContext time), entropy is room-seeded,
// sleep cannot block the actor, and stdio lands in the room log. The system
// clock, system entropy, filesystem, and network are unreachable.
//
// The entropy source is wrapped in a counting reader (snapshot determinism): a
// fresh room starts the seeded stream at byte 0; a restore re-seeds the stream
// to the snapshot's consumed position via resumeEntropy AFTER the runtime is
// primed (so the runtime-init draw is not double-counted).
func (h *wasmHandler) moduleConfig(seed int64) wazero.ModuleConfig {
	logw := &logWriter{h: h}
	h.entropy = &countingReader{r: rand.New(rand.NewSource(seed))}
	return wazero.NewModuleConfig().
		WithWalltime(func() (int64, int32) {
			ns := h.nowNanos
			return ns / 1e9, int32(ns % 1e9)
		}, wzsys.ClockResolution(1)).
		WithNanotime(func() int64 { return h.nowNanos }, wzsys.ClockResolution(1)).
		WithNanosleep(func(int64) {}). // sleep is a no-op: use wake + CallContext time
		WithRandSource(h.entropy).
		WithStdout(logw).
		WithStderr(logw)
}

// resumeEntropy re-points the entropy reader at the seeded stream's `consumed`
// position, discarding whatever the runtime-init prime drew. After this the
// guest draws exactly the bytes it would have next drawn at snapshot time.
func (h *wasmHandler) resumeEntropy(seed, consumed int64) {
	src := rand.New(rand.NewSource(seed))
	if consumed > 0 {
		_, _ = io.CopyN(io.Discard, src, consumed)
	}
	h.entropy.r = src
	h.entropy.n = consumed
}

// logWriter pipes guest stdout/stderr into the room log. Guest code only runs
// during callbacks, so h.cur is set whenever a write can occur; instantiation
// output (before the first callback) falls back to the services logger.
// stdout/stderr is author-debug data, not operator data, so it lands at Debug —
// truncated per write and metered by the per-room guest log budget.
type logWriter struct{ h *wasmHandler }

func (w *logWriter) Write(p []byte) (int, error) {
	msg := truncateGuestLog(string(p))
	if w.h.guestLogAllow(len(msg)) {
		if log := w.h.guestLogger(); log != nil {
			log.Debug("guest", "out", msg)
		}
	}
	return len(p), nil
}

// guestLogger returns the room log mid-callback, else the services logger
// (instantiation output lands before the first callback). May be nil.
func (h *wasmHandler) guestLogger() *slog.Logger {
	if h.cur != nil {
		return h.cur.Log()
	}
	return h.svc.Log
}

// truncateGuestLog caps one guest write/log call at guestLogMaxWrite bytes.
func truncateGuestLog(s string) string {
	if len(s) <= guestLogMaxWrite {
		return s
	}
	return s[:guestLogMaxWrite] + "…[truncated]"
}

// guestLogAllow charges n bytes against the room's guest log budget, reporting
// whether the write may be emitted. The first refusal per window emits one Warn
// marker so operators still see that limiting happened. Real wall clock (not
// the virtualized room clock — the budget meters host log volume per real
// second). Actor-goroutine owned, no locking.
func (h *wasmHandler) guestLogAllow(n int) bool {
	now := time.Now()
	if now.Sub(h.logWindowStart) >= guestLogWindow {
		h.logWindowStart, h.logBytes, h.logLimited = now, 0, false
	}
	h.logBytes += n
	if h.logBytes <= guestLogBudget {
		return true
	}
	if !h.logLimited {
		h.logLimited = true
		if log := h.guestLogger(); log != nil {
			log.Warn("gameabi: guest log output rate-limited", "slug", h.game.meta.Slug)
		}
	}
	return false
}

func (h *wasmHandler) OnJoin(r sdk.Room, p sdk.Player)  { h.callWithPlayer(r, wire.ExpJoin, p) }
func (h *wasmHandler) OnLeave(r sdk.Room, p sdk.Player) { h.callWithPlayer(r, wire.ExpLeave, p) }

func (h *wasmHandler) OnInput(r sdk.Room, p sdk.Player, in sdk.Input) {
	idx, roster := rosterWith(r.Members(), p)
	var w wire.Buf
	w.U32(uint32(idx))
	if in.Kind == sdk.InputRune {
		w.U8(wire.InputRune)
	} else {
		w.U8(wire.InputKey)
	}
	w.U32(uint32(in.Rune))
	w.U8(uint8(in.Key))
	// Bytes in (add-metrics): the normalized input payload the host delivers —
	// host-measured, the module never reports its own numbers.
	if h.game.opts.Metrics != nil {
		h.game.opts.Metrics.GameInputBytesIn(h.game.meta.Slug, len(w.B))
	}
	h.invoke(r, wire.ExpInput, roster, w.B)
}

func (h *wasmHandler) OnTick(r sdk.Room, now time.Time) {
	// Sample memory each heartbeat, AFTER the wake so growth the beat itself
	// caused is captured — and on the empty-room early return too: an idle
	// room (no members, not yet frozen) still pins its grown linear memory
	// against GOMEMLIMIT, and the gauge must keep saying so.
	defer h.sampleMemory()
	if len(r.Members()) == 0 && h.cfg.Lifecycle != sdk.LifecycleResident {
		return // no heartbeat for an empty room (resident worlds keep ticking)
	}
	h.call(r, wire.ExpWake, nil)
}

// sampleMemory folds the room's current guest linear-memory size into the
// per-game memory gauge, as a delta against the last sample, so the exported
// series is the SUM across the game's live rooms (no per-room series — the
// metrics package's low-cardinality rule). Host-measured from the wazero
// instance; heartbeat-sampled on the actor goroutine.
func (h *wasmHandler) sampleMemory() {
	m := h.game.opts.Metrics
	if m == nil || h.inst == nil {
		return
	}
	mem := guestMemory(h.inst)
	if mem == nil {
		return
	}
	if size := mem.Size(); size != h.memSampled {
		m.GameLinearMemoryDelta(h.game.meta.Slug, int64(size)-int64(h.memSampled))
		h.memSampled = size
	}
}

func (h *wasmHandler) OnClose(r sdk.Room) {
	if h.cur == nil { // never re-enter a guest already on the stack
		h.call(r, wire.ExpClose, nil)
	}
	h.closeInstance()
}

// closeInstance releases the plugin instance, deferring to the end of the
// in-flight guest call if one is on the stack (a synchronous room driver can
// settle the room from inside the end host function).
func (h *wasmHandler) closeInstance() {
	if h.cur != nil {
		h.closePending = true
		return
	}
	if h.inst != nil {
		_ = h.inst.Close(context.Background())
		h.inst = nil
	}
	// Retire this room's contribution to the per-game memory gauge — the
	// closed instance's linear memory is released back to the Go heap.
	if h.memSampled != 0 {
		if m := h.game.opts.Metrics; m != nil {
			m.GameLinearMemoryDelta(h.game.meta.Slug, -int64(h.memSampled))
		}
		h.memSampled = 0
	}
}

// callWithPlayer encodes join/leave: the player rides as a roster index; for
// leave (already removed from Members) the departed player is appended as the
// final roster entry so kv writes for the leaver still resolve.
func (h *wasmHandler) callWithPlayer(r sdk.Room, name string, p sdk.Player) {
	idx, roster := rosterWith(r.Members(), p)
	var w wire.Buf
	w.U32(uint32(idx))
	h.invoke(r, name, roster, w.B)
}

func (h *wasmHandler) call(r sdk.Room, name string, extra []byte) {
	h.invoke(r, name, r.Members(), extra)
}

// invoke runs one guest callback: CallContext || extra as the input payload,
// with the handler reachable from host functions via the call context.
func (h *wasmHandler) invoke(r sdk.Room, name string, roster []sdk.Player, extra []byte) {
	if h.inst == nil || h.dead || h.cur != nil {
		return // dead instance, or a guest call is already on the stack
	}
	_, settled := r.Result()
	h.nowNanos = r.Now().UnixNano() // keep the virtualized clock == CallContext time

	// Roster mutation (join/leave/index shift/mid-join) renumbers slots, so any
	// surviving guest baseline would diff against a stale host baseline. Detect a
	// roster delta BEFORE encoding (the roster epoch must bump first), and
	// invalidate EVERY baseline slot so the next send to each slot is
	// epoch-rejected into a keyframe (D7/4.6). This is the host-authority
	// backstop layered under the per-send epoch check.
	if fp := rosterFingerprint(roster); fp != h.rosterFP {
		h.rosterFP = fp
		h.baselines.invalidateAll()
		h.rosterEpoch++
	}

	var w *wire.Buf
	features := h.game.meta.CtxFeatures
	if h.epochMode {
		// Roster-epoch mode: full roster only when this instance hasn't seen
		// the current epoch (mutation, first callback, post-restore); a
		// 6-byte unchanged marker otherwise — the member list isn't even
		// built on that path (the O(members)-per-callback win).
		full := h.forceFullRoster || h.rosterEpoch != h.lastFullEpoch
		w = encodeCtxEpoch(h.nowNanos, h.cfg, roster, settled, h.rosterEpoch, full, features)
		if full {
			h.lastFullEpoch = h.rosterEpoch
			h.forceFullRoster = false
		}
	} else {
		w = encodeCtx(h.nowNanos, h.cfg, roster, settled, features)
	}
	payload := append(w.B, extra...)

	h.cur, h.roster = r, roster
	defer func() {
		// h.cur is callback-scoped (the Room handle is invalid after the call),
		// but h.roster is left set as the LAST-SEEN roster so a quiescent
		// Snapshot can record the room's membership between callbacks.
		h.cur = nil
		if h.closePending {
			h.closePending = false
			h.closeInstance()
		}
	}()

	ctx := context.WithValue(context.Background(), handlerKey{}, h)
	ctx, cancel := context.WithTimeout(ctx, h.deadline) // kill switch, not a budget (per-room)
	defer cancel()
	hostStart := h.cbHostNanos // per-callback host-time delta baseline
	h.hostIOExpired = false
	cbStart := time.Now()
	exit, _, err := h.inst.CallWithContext(ctx, name, payload)
	dur := time.Since(cbStart)
	h.cbTotalNanos += dur.Nanoseconds()
	hostDelta := h.cbHostNanos - hostStart
	h.lastExit, h.lastErr = exit, err
	// The kill switch firing is the deadline signal: the callback errored AND
	// the per-callback timeout (not the parent ctx) elapsed. Discriminate WHOSE
	// time was burned: a deadline that expired while the guest was blocked in
	// the host's own store/config call, with host time dominating the callback
	// wall clock, is a host-I/O incident (slow shared Postgres) — the room must
	// still end (wazero condemned the instance at the deadline) but it is NOT a
	// game fault, so DB slowness never feeds quarantine. The guest cannot game
	// this into quarantine evasion: a spin-then-kv guest accrues its time as
	// guest-pure, failing the dominance check.
	deadlined := err != nil && ctx.Err() == context.DeadlineExceeded
	hostIOKill := deadlined && h.hostIOExpired && 2*hostDelta >= dur.Nanoseconds()
	if m := h.game.opts.Metrics; m != nil {
		m.GameCallback(h.game.meta.Slug, name, dur.Seconds())
		if hostIOKill {
			m.GameHostIODeadline(h.game.meta.Slug, name)
		} else if deadlined {
			m.GameCallbackDeadline(h.game.meta.Slug, name)
		}
	}
	if err != nil || exit != 0 {
		h.dead = true
		if hostIOKill {
			r.Log().Error("gameabi: host call outlived the callback deadline — settling room without fault",
				"callback", name, "hostNanos", hostDelta, "callbackNanos", dur.Nanoseconds(), "err", err)
			r.End(sdk.Result{Mode: h.cfg.Mode})
			return
		}
		r.Log().Error("gameabi: guest trap — settling room", "callback", name, "exit", exit, "err", err)
		h.fault()
		r.End(sdk.Result{Mode: h.cfg.Mode})
		return
	}
	h.publishPhase(r, settled)
}

// fault reports a guest fault to the load-time hook (quarantine accounting)
// and the fault counter (add-metrics).
func (h *wasmHandler) fault() {
	if h.game.opts.Metrics != nil {
		h.game.opts.Metrics.GameFault(h.game.meta.Slug)
	}
	if h.game.opts.OnFault != nil {
		h.game.opts.OnFault(h.game.meta.Slug)
	}
}

// publishPhase derives the lobby-visible phase for a wasm room — the ABI has
// no phase surface, so the host owns joinability: open ⇔ unsettled ∧ below
// capacity. Published after every callback so join/leave/end are reflected
// immediately; the engine's terminal "settled" phase is never overwritten.
func (h *wasmHandler) publishPhase(r sdk.Room, settled bool) {
	if settled || h.ended {
		return
	}
	open := h.cfg.Capacity == 0 || len(r.Members()) < h.cfg.Capacity
	r.SetPhase("in play", open, time.Time{})
}

// rosterWith returns members plus p (appended if absent) and p's index.
func rosterWith(members []sdk.Player, p sdk.Player) (int, []sdk.Player) {
	for i, m := range members {
		if m == p {
			return i, members
		}
	}
	return len(members), append(append([]sdk.Player{}, members...), p)
}

// ---- host functions ---------------------------------------------------------

func currentHandler(ctx context.Context) *wasmHandler {
	h, _ := ctx.Value(handlerKey{}).(*wasmHandler)
	return h
}

// hf builds a stack-based host function in the shellcade namespace.
func hf(name string, params, returns []extism.ValueType, fn extism.HostFunctionStackCallback) extism.HostFunction {
	f := extism.NewHostFunctionWithStack(name, fn, params, returns)
	f.SetNamespace(wire.HostNamespace)
	return f
}

var (
	ptr = extism.ValueTypePTR
	i64 = extism.ValueTypeI64
)

func hostFunctions() []extism.HostFunction {
	return []extism.HostFunction{
		// send(i64 playerIdx, ptr deltaContainer) -> i64 epoch. The payload is the
		// v2 delta container (wire §4.5), applied to the per-index baseline under
		// the epoch authority (D4/D5). The return value's low 32 bits carry the
		// epoch the guest must stamp; the upper 32 bits are reserved-zero.
		hf("send", []extism.ValueType{i64, ptr}, []extism.ValueType{i64},
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				if h == nil || h.cur == nil {
					stack[0] = 0
					return
				}
				hfStart := time.Now()
				defer func() { h.cbHostNanos += time.Since(hfStart).Nanoseconds() }()
				idx := int(stack[0])
				b, err := p.ReadBytes(stack[1])
				if err != nil || idx < 0 || idx >= rosterCap || idx >= len(h.roster) {
					// Out-of-range index: there is no slot to advance; return its
					// current epoch (0 if never issued) without mutating the cache.
					stack[0] = 0
					return
				}
				res := h.baselines.apply(idx, b, func(reason string) {
					h.cur.Log().Warn("gameabi: dropped malformed delta", "err", reason)
				})
				if res.applied {
					if g, derr := decodeFrame(h.baselines.prev[idx]); derr == nil {
						h.cur.Send(h.roster[idx], g)
						// Bytes out (add-metrics): wire bytes the host accepted, once
						// per frame produced. Under ABI v2 this is the delta container,
						// so the metric measures real guest wire output (tens of bytes
						// steady-state), not a constant full frame.
						if h.game.opts.Metrics != nil {
							h.game.opts.Metrics.GameFrameBytesOut(h.game.meta.Slug, len(b))
						}
					} else {
						h.cur.Log().Warn("gameabi: dropped undecodable reconstructed frame", "err", derr)
					}
				}
				stack[0] = uint64(res.epoch)
			}),
		// identical(ptr deltaContainer) -> i64 epoch. Diffed against the broadcast
		// slot; on apply the reconstructed grid is copied into EVERY per-index
		// baseline and every slot's epoch is set to the broadcast epoch (D7).
		hf("identical", []extism.ValueType{ptr}, []extism.ValueType{i64},
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				if h == nil || h.cur == nil {
					stack[0] = 0
					return
				}
				hfStart := time.Now()
				defer func() { h.cbHostNanos += time.Since(hfStart).Nanoseconds() }()
				b, err := p.ReadBytes(stack[0])
				if err != nil {
					stack[0] = uint64(h.baselines.bump(broadcastSlot))
					return
				}
				res := h.baselines.apply(broadcastSlot, b, func(reason string) {
					h.cur.Log().Warn("gameabi: dropped malformed delta", "err", reason)
				})
				if res.applied {
					if g, derr := decodeFrame(h.baselines.prev[broadcastSlot]); derr == nil {
						h.cur.Identical(g)
						// Reconcile every per-index baseline so a later per-player
						// Send diffs against the broadcast baseline (D7).
						h.baselines.reconcileBroadcast(res.epoch)
						// Bytes out (add-metrics): counted ONCE per frame produced here
						// too — fan-out to the roster is delivery, not production. Under
						// ABI v2 this is the delta container's wire bytes.
						if h.game.opts.Metrics != nil {
							h.game.opts.Metrics.GameFrameBytesOut(h.game.meta.Slug, len(b))
						}
					} else {
						h.cur.Log().Warn("gameabi: dropped undecodable reconstructed frame", "err", derr)
					}
				}
				stack[0] = uint64(res.epoch)
			}),
		hf("set_input_context", []extism.ValueType{i64}, nil,
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				if h == nil || h.cur == nil {
					return
				}
				h.inputCtx = sdk.InputContext(stack[0]) // mirrored for snapshots
				h.cur.SetInputContext(h.inputCtx)
			}),
		hf("end", []extism.ValueType{ptr}, nil,
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				if h == nil || h.cur == nil {
					return
				}
				b, _ := p.ReadBytes(stack[0])
				res, err := decodeResult(b, h.roster, h.cfg.Mode)
				if err != nil {
					h.cur.Log().Warn("gameabi: bad end payload", "err", err)
					res = sdk.Result{Mode: h.cfg.Mode}
				}
				h.ended = true
				h.cur.End(res)
			}),
		hf("post", []extism.ValueType{ptr}, nil,
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				if h == nil || h.cur == nil || h.svc.Leaderboard == nil {
					return
				}
				b, _ := p.ReadBytes(stack[0])
				res, err := decodeResult(b, h.roster, h.cfg.Mode)
				if err != nil {
					return
				}
				// Stamp the room-scoped post sequence (mirrored for snapshots):
				// the durable leaderboard derives an idempotent round id from
				// (roomID, RoundSeq), so a re-settle after a pre-settle reclaim
				// — which replays this exact counter value — dedupes instead of
				// double-counting (and a retried write inserts nothing twice).
				h.postSeq++
				res.RoundSeq = uint64(h.postSeq)
				h.svc.Leaderboard.Post(h.game.meta.Slug, res)
			}),
		hf("log", []extism.ValueType{i64, ptr}, nil,
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				if h == nil || h.cur == nil {
					return
				}
				msg, _ := p.ReadString(stack[1])
				msg = truncateGuestLog(msg)
				if !h.guestLogAllow(len(msg)) {
					return
				}
				switch stack[0] {
				case 0:
					h.cur.Log().Debug(msg)
				case 2:
					h.cur.Log().Warn(msg)
				case 3:
					h.cur.Log().Error(msg)
				default:
					h.cur.Log().Info(msg)
				}
			}),
		// The kv/config host functions all follow one shape: the store context is
		// DERIVED from the callback ctx (capped at kvTimeout) so a slow Postgres
		// cancels with the kill switch instead of stalling the room actor; the
		// blocked time is accounted as HOST time (cbHostNanos, like send/identical)
		// and a kill-switch expiry during the store call is flagged so the trap
		// path books it as a host-I/O incident, not a game fault; and store errors
		// are logged with slug/account/key (+ a metrics counter) instead of
		// vanishing — the guest still sees the ABI's silent absent/dropped result.
		hf("kv_get", []extism.ValueType{i64, ptr}, []extism.ValueType{ptr},
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				idx := int(stack[0])
				store := h.kvStore(idx)
				key, _ := p.ReadString(stack[1])
				stack[0] = 0
				if store == nil || key == "" {
					return
				}
				hfStart := time.Now()
				defer func() { h.cbHostNanos += time.Since(hfStart).Nanoseconds() }()
				cctx, cancel := context.WithTimeout(ctx, kvTimeout)
				defer cancel()
				v, ok, err := store.Get(cctx, key)
				if ctx.Err() != nil {
					h.hostIOExpired = true
				}
				if err != nil {
					h.kvFailed("kv_get", idx, key, err)
					return
				}
				if !ok {
					return
				}
				off, werr := p.WriteBytes(v)
				if werr == nil {
					stack[0] = off
				}
			}),
		hf("kv_set", []extism.ValueType{i64, ptr, ptr, ptr}, nil,
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				idx := int(stack[0])
				store := h.kvStore(idx)
				key, _ := p.ReadString(stack[1])
				val, _ := p.ReadBytes(stack[2])
				rule, _ := p.ReadString(stack[3])
				if store == nil || key == "" {
					return
				}
				hfStart := time.Now()
				defer func() { h.cbHostNanos += time.Since(hfStart).Nanoseconds() }()
				cctx, cancel := context.WithTimeout(ctx, kvTimeout)
				defer cancel()
				err := store.Set(cctx, key, val, sdk.MergeRule(rule))
				if ctx.Err() != nil {
					h.hostIOExpired = true
				}
				if err != nil {
					h.kvFailed("kv_set", idx, key, err)
				}
			}),
		hf("kv_delete", []extism.ValueType{i64, ptr}, nil,
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				idx := int(stack[0])
				store := h.kvStore(idx)
				key, _ := p.ReadString(stack[1])
				if store == nil || key == "" {
					return
				}
				hfStart := time.Now()
				defer func() { h.cbHostNanos += time.Since(hfStart).Nanoseconds() }()
				cctx, cancel := context.WithTimeout(ctx, kvTimeout)
				defer cancel()
				err := store.Delete(cctx, key)
				if ctx.Err() != nil {
					h.hostIOExpired = true
				}
				if err != nil {
					h.kvFailed("kv_delete", idx, key, err)
				}
			}),
		hf("config_get", []extism.ValueType{ptr}, []extism.ValueType{ptr},
			func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
				h := currentHandler(ctx)
				key, _ := p.ReadString(stack[0])
				stack[0] = 0
				if h == nil || h.cur == nil || h.svc.Config == nil || key == "" {
					return
				}
				hfStart := time.Now()
				defer func() { h.cbHostNanos += time.Since(hfStart).Nanoseconds() }()
				cctx, cancel := context.WithTimeout(ctx, kvTimeout)
				defer cancel()
				v, ok, err := h.svc.Config.Get(cctx, key)
				if ctx.Err() != nil {
					h.hostIOExpired = true
				}
				if err != nil {
					h.kvFailed("config_get", -1, key, err)
					return
				}
				if !ok {
					return
				}
				off, werr := p.WriteBytes(v)
				if werr == nil {
					stack[0] = off
				}
			}),
	}
}

// kvFailed surfaces a kv/config host-call store error: logged with
// slug/account/key plus a metrics counter, so a dropped write or a conflated
// "absent" read is never silent host-side. idx < 0 (config_get) carries no
// account. Called only mid-callback (the host functions verified h.cur).
func (h *wasmHandler) kvFailed(op string, idx int, key string, err error) {
	if m := h.game.opts.Metrics; m != nil {
		m.GameKVError(h.game.meta.Slug, op)
	}
	acct := ""
	if idx >= 0 && idx < len(h.roster) {
		acct = h.roster[idx].AccountID
	}
	h.cur.Log().Error("gameabi: "+op+" failed",
		"slug", h.game.meta.Slug, "account", acct, "key", key, "err", err)
}

// kvStore resolves the per-user KV for a roster index (host-side scoping: the
// guest names only the index; account + game namespace are derived here).
func (h *wasmHandler) kvStore(idx int) sdk.KVStore {
	if h == nil || h.cur == nil || h.svc.Accounts == nil {
		return nil
	}
	if idx < 0 || idx >= len(h.roster) {
		return nil
	}
	acct := h.svc.Accounts.For(h.roster[idx])
	if acct == nil {
		return nil
	}
	return acct.Store()
}

// countingReader wraps the seeded entropy source and tallies the bytes the guest
// has drawn, so Snapshot can record the stream position and Restore can replay
// the deterministic source to exactly that point. Access is serialized to the
// room actor goroutine (entropy is drawn only inside a guest callback, and a
// snapshot is taken at a quiescent point on the same goroutine), so the counter
// needs no lock.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// consumed reports how many entropy bytes the guest has drawn so far (0 if the
// source was never wired — e.g. before OnStart).
func (h *wasmHandler) consumed() int64 {
	if h.entropy == nil {
		return 0
	}
	return h.entropy.n
}
