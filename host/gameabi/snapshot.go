package gameabi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"unsafe"

	extism "github.com/extism/go-sdk"
	"github.com/klauspost/compress/zstd"
	"github.com/tetratelabs/wazero/api"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/sdk"
)

// guestMemory returns the GUEST module's linear memory — the wasm program's own
// TinyGo heap and data section, which is where all persistent room state lives.
//
// extism.Plugin.Memory() returns the EXTISM RUNTIME module's memory (a separate
// instance holding only the transient per-call input/output buffers), NOT the
// guest's. The guest module (p.mainModule) is unexported and Plugin.Module()
// wraps it without surfacing Memory(), so we reach the api.Module by reflection
// and then use its public Memory() — the only supported field we need, read
// once per snapshot/restore. If extism ever exposes the main module's memory
// publicly, this helper is the single place to switch over.
func guestMemory(p *extism.Plugin) api.Memory {
	v := reflect.ValueOf(p).Elem().FieldByName("mainModule")
	if !v.IsValid() {
		return nil
	}
	v = reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	mod, ok := v.Interface().(api.Module)
	if !ok || mod == nil {
		return nil
	}
	return mod.Memory()
}

// Snapshot/Restore (D9 hibernation, ABI task 6.1): freeze a live wasm room into
// a portable blob and rehydrate it into a fresh handler. A snapshot captures the
// guest's full linear memory plus the host state needed to resume
// deterministically — roster, room clock, terminal flags, current input
// context, and the entropy stream
// position (so the seeded source replays to the exact byte the guest
// last drew). The blob is zstd-compressed; linear memory is mostly zero pages,
// so it compresses hard.
//
// Hibernation TRIGGERS (when to snapshot, where the blob is stored, how a room
// is resumed in the lobby) are a later lane — this lane provides only the codec
// the engine will call. Snapshot must be taken at a quiescent point: no guest
// call on the stack (h.cur == nil), so linear memory and the entropy counter
// are not mid-mutation.

// snapshotMagic + snapshotFormat version the host blob independently of the ABI
// (a blob layout change bumps the format without touching the wasm ABI).
//
// format 2 adds the room's Mode/Capacity/MinPlayers. The guest sees the full
// RoomConfig in every callback's CallContext (encodeCtx), so a restore that
// dropped these fields handed the resumed guest a DIFFERENT context than the
// control — diverging any guest whose behavior reads them (directly, or via the
// per-callback context the seeded RNG + allocations are laid out against). The
// fixture, which ignores RoomConfig entirely, was blind to the loss; a real game
// (pokies) surfaced it as a hibernation-determinism failure.
//
// format 3 (ABI v2 frame diffing, D6) appends a u32 epoch HIGH-WATER: the max
// frame-delta epoch the host had issued before the snapshot. On resume the host
// seeds epochSeq strictly above it so every snapshot-surviving guest epoch is
// stale and the guest's first post-restore send per consumer is epoch-rejected
// into a keyframe. The host-side baseline CACHE itself is ephemeral host memory
// and is deliberately NOT snapshotted; only this single counter crosses.
//
// format 4 (leaderboard idempotency) appends the u32 leaderboard post-sequence
// counter. The durable leaderboard derives a round id deterministically from
// (roomID, postSeq), so the counter MUST survive a restore: a room reclaimed
// from a pre-settle checkpoint that re-settles the same round replays the same
// sequence and the insert dedupes instead of double-counting. A format-3 blob
// that dropped the counter would reset it to 0 and re-mint colliding sequences
// for rounds already recorded under DIFFERENT (post-format-4) ids — so old
// blobs are refused, like every prior format bump.
const (
	snapshotMagic  = 0x53434b31 // "SCK1"
	snapshotFormat = 4
)

// ErrArtifactMismatch is returned (wrapped) by Restore when the snapshot was
// taken under a DIFFERENT wasm artifact than the game it is being restored
// into — the inevitable outcome of a catalog promotion or rollback moving the
// slug's live-version pointer while rooms were parked. The blob itself is
// intact; it is the live game that moved. Callers use errors.Is to tell this
// apart from genuine corruption: the lobby surfaces "your saved game was
// retired by a game update" instead of a corrupt/expired notice, and the
// resident bring-up logs the discarded drain snapshot explicitly.
var ErrArtifactMismatch = errors.New("artifact mismatch")

// snapState is the decoded host blob (everything but the linear-memory bytes,
// which the caller handles as a length-prefixed raw region).
type snapState struct {
	abiVersion uint32
	artifact   [sha256.Size]byte
	nowNanos   int64
	seed       int64
	consumed   int64 // entropy bytes drawn
	ended      bool
	dead       bool
	inputCtx   uint8
	mode       sdk.Mode // room mode (sent to the guest in every CallContext)
	capacity   int32
	minPlayers int32
	epochHW    uint32 // frame-delta epoch high-water (format 3; D6 resume re-seed)
	postSeq    uint32 // leaderboard post-sequence counter (format 4; round-id idempotency)
	roster     []sdk.Player
}

// Snapshot freezes handler h into a compressed, self-describing blob. It MUST be
// called at a quiescent point (no guest callback on the stack) — Snapshot
// enforces this and refuses an in-flight handler. A dead instance (faulted /
// closed) cannot be snapshotted.
func (h *wasmHandler) Snapshot() ([]byte, error) {
	if h.cur != nil {
		return nil, fmt.Errorf("gameabi: snapshot during a guest call (not quiescent)")
	}
	if h.inst == nil {
		return nil, fmt.Errorf("gameabi: snapshot of a closed/never-started room")
	}
	mem := guestMemory(h.inst)
	if mem == nil {
		return nil, fmt.Errorf("gameabi: snapshot: no guest linear memory")
	}
	size := mem.Size()
	view, ok := mem.Read(0, size)
	if !ok {
		return nil, fmt.Errorf("gameabi: snapshot: memory read failed (size %d)", size)
	}
	// Read returns a write-through VIEW into the live module — copy it before the
	// instance can mutate it again.
	linmem := make([]byte, len(view))
	copy(linmem, view)

	var w wire.Buf
	w.U32(snapshotMagic)
	w.U32(snapshotFormat)
	w.U32(Version)
	w.B = append(w.B, h.game.wasmSHA[:]...)
	w.I64(h.nowNanos)
	w.I64(h.seed)
	w.I64(h.consumed())
	w.Bool(h.ended)
	w.Bool(h.dead)
	w.U8(uint8(h.inputCtx))
	// Full RoomConfig (minus Seed, carried above): the guest reads Mode,
	// Capacity, and MinPlayers from the CallContext on every callback, so they
	// must survive a restore byte-for-byte or the resumed guest diverges.
	w.Str(string(h.cfg.Mode))
	w.U32(uint32(int32(h.cfg.Capacity)))
	w.U32(uint32(int32(h.cfg.MinPlayers)))
	// Frame-delta epoch high-water (format 3, D6): the max epoch the host issued
	// before this snapshot. On resume the host seeds epochSeq strictly above it.
	// The baseline cache itself is ephemeral host memory and is NOT snapshotted.
	w.U32(h.baselines.epochSeq)
	// Leaderboard post-sequence counter (format 4): must survive a restore so a
	// re-settled round re-derives the SAME round id and dedupes (idempotency).
	w.U32(h.postSeq)
	// Character is deliberately NOT snapshotted: h.roster is overwritten with
	// the live roster on every invoke before any consumer reads it, and the
	// post-restore forceFullRoster resend carries live characters — the
	// restored roster only seeds the fingerprint and quiescent re-snapshot.
	w.U16(uint16(len(h.roster)))
	for _, p := range h.roster {
		w.Str(p.Handle)
		w.Str(p.AccountID)
		w.Str(p.Conn)
		w.Str(string(p.Kind))
	}
	w.U32(uint32(len(linmem)))
	w.B = append(w.B, linmem...)

	return zstdEncode(w.B), nil
}

// Restore decompresses a blob and rehydrates it into a FRESH handler bound to
// game g (g must be the same artifact the blob was taken from — the embedded
// sha256 + ABI version are verified). The returned handler holds a live instance
// resumed at the snapshot's memory, clock, roster, input context, and entropy
// position; the next callback continues exactly where the snapshot left off.
//
// The handler is NOT yet attached to a Room (h.cur == nil); the engine drives it
// the same way the actor drives a fresh handler.
func (g *wasmGame) Restore(blob []byte) (*wasmHandler, error) {
	raw, err := zstdDecode(blob)
	if err != nil {
		return nil, fmt.Errorf("gameabi: restore: decompress: %w", err)
	}
	st, linmem, err := decodeSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if st.abiVersion != Version {
		return nil, fmt.Errorf("gameabi: restore: blob targets ABI v%d, host implements v%d", st.abiVersion, Version)
	}
	if st.artifact != g.wasmSHA {
		return nil, fmt.Errorf("gameabi: restore: %w (blob %x… vs game %x…)", ErrArtifactMismatch, st.artifact[:4], g.wasmSHA[:4])
	}

	// Reconstruct the FULL RoomConfig the room ran with: the guest reads Mode,
	// Capacity, and MinPlayers from the per-callback CallContext, so a restore
	// that resumed with a partial cfg would feed the guest a different context
	// and diverge (hibernation-determinism failure).
	cfg := sdk.RoomConfig{
		Mode:       st.mode,
		Capacity:   int(st.capacity),
		MinPlayers: int(st.minPlayers),
		Seed:       st.seed,
		SeedSet:    true,
	}
	h := &wasmHandler{
		game:      g,
		cfg:       cfg,
		heartbeat: g.opts.Heartbeat,
		deadline:  g.opts.CallbackDeadline,
		epochMode: g.meta.CtxFeatures&wire.CtxFeatRosterEpoch != 0,
		// rosterFP is seeded from the snapshot roster below, so the
		// fingerprint bump won't fire on a same-roster resume — force the
		// first post-restore callback to the full form explicitly.
		forceFullRoster: g.meta.CtxFeatures&wire.CtxFeatRosterEpoch != 0,
		seed:      st.seed,
		nowNanos:  st.nowNanos,
		ended:     st.ended,
		dead:      st.dead,
		inputCtx:  sdk.InputContext(st.inputCtx),
		postSeq:   st.postSeq,
		roster:    st.roster,
	}
	// Roster-epoch mode: rosterEpoch/lastFullEpoch are ephemeral host memory
	// (zero on this fresh handler), so the first post-restore callback always
	// carries the 0xFFFE full form — the guest re-caches unconditionally and
	// no cross-restore epoch reasoning is needed. (h.rosterFP is also zero,
	// so the first callback's fingerprint mismatch bumps rosterEpoch to 1.)
	// D6 hibernation resync: the host's baseline cache is ephemeral host memory
	// and was NOT snapshotted, so it starts fresh (every slot not-present). Seed
	// its epoch counter strictly above the pre-snapshot high-water so every
	// snapshot-surviving GUEST epoch is now stale: the restored guest's first
	// send per consumer (a delta against its surviving baseline, stamped with its
	// surviving epoch) epoch-mismatches and is rejected, forcing a keyframe. The
	// engine's OnResume re-applies this; seeding here makes a directly-driven
	// restore (tests, and any non-OnResume driver) correct too. rosterFP is also
	// seeded from the restored roster so a same-roster resume is NOT mistaken for
	// a roster mutation (its own invalidateAll would be harmless but redundant).
	h.baselines.reseed(st.epochHW)
	h.rosterFP = rosterFingerprint(st.roster)

	// Instantiate with the same virtualized WASI surface and a seeded entropy
	// source (positioned by resumeEntropy below, after the runtime is primed).
	inst, err := g.compiled.Instance(context.Background(),
		extism.PluginInstanceConfig{ModuleConfig: h.moduleConfig(st.seed)})
	if err != nil {
		return nil, fmt.Errorf("gameabi: restore: instantiate: %w", err)
	}

	// Prime the guest runtime BEFORE overwriting memory: extism runs the wasm
	// `_initialize` lazily on the first non-start Call, which would re-zero the
	// data section (the kit room handler global, the wake counter, …) and clobber
	// the restored memory. A throwaway `shellcade_abi` call forces init to run
	// now, so the subsequent Write is the final word and later callbacks skip it.
	if _, _, err := inst.CallWithContext(context.Background(), wire.ExpABI, nil); err != nil {
		_ = inst.Close(context.Background())
		return nil, fmt.Errorf("gameabi: restore: prime runtime: %w", err)
	}
	// Now re-seek the entropy stream to the snapshot position, discarding the
	// bytes the prime's runtime-init drew (otherwise the stream would be off by
	// the init draw and the resumed guest would diverge).
	h.resumeEntropy(st.seed, st.consumed)

	// Grow the fresh GUEST memory to the snapshot size, then overwrite it with
	// the captured bytes.
	mem := guestMemory(inst)
	if mem == nil {
		_ = inst.Close(context.Background())
		return nil, fmt.Errorf("gameabi: restore: no guest linear memory")
	}
	if cur := mem.Size(); cur < uint32(len(linmem)) {
		const pageSize = 64 * 1024
		need := (uint32(len(linmem)) - cur + pageSize - 1) / pageSize
		if _, ok := mem.Grow(need); !ok {
			_ = inst.Close(context.Background())
			return nil, fmt.Errorf("gameabi: restore: grow memory to %d bytes failed", len(linmem))
		}
	}
	if ok := mem.Write(0, linmem); !ok {
		_ = inst.Close(context.Background())
		return nil, fmt.Errorf("gameabi: restore: write %d bytes failed", len(linmem))
	}
	h.inst = inst
	return h, nil
}

// decodeSnapshot reads the host blob, returning the decoded state and the raw
// linear-memory bytes (a slice into raw — the caller copies on Write).
func decodeSnapshot(raw []byte) (snapState, []byte, error) {
	r := wire.Rd{B: raw}
	if r.U32() != snapshotMagic {
		return snapState{}, nil, fmt.Errorf("gameabi: snapshot: bad magic")
	}
	if f := r.U32(); f != snapshotFormat {
		return snapState{}, nil, fmt.Errorf("gameabi: snapshot: format v%d, want v%d", f, snapshotFormat)
	}
	var st snapState
	st.abiVersion = r.U32()
	if r.Off+sha256.Size > len(r.B) {
		return snapState{}, nil, fmt.Errorf("gameabi: snapshot: truncated artifact hash")
	}
	copy(st.artifact[:], r.B[r.Off:r.Off+sha256.Size])
	r.Off += sha256.Size
	st.nowNanos = r.I64()
	st.seed = r.I64()
	st.consumed = r.I64()
	st.ended = r.Bool()
	st.dead = r.Bool()
	st.inputCtx = r.U8()
	st.mode = sdk.Mode(r.Str())
	st.capacity = int32(r.U32())
	st.minPlayers = int32(r.U32())
	st.epochHW = r.U32() // format 3: frame-delta epoch high-water (D6)
	st.postSeq = r.U32() // format 4: leaderboard post-sequence counter
	n := int(r.U16())
	for i := 0; i < n; i++ {
		p := sdk.Player{
			Handle:    r.Str(),
			AccountID: r.Str(),
			Conn:      r.Str(),
			Kind:      sdk.Kind(r.Str()),
		}
		st.roster = append(st.roster, p)
	}
	memLen := int(r.U32())
	if err := r.Err(); err != nil {
		return snapState{}, nil, fmt.Errorf("gameabi: snapshot: %w", err)
	}
	if memLen < 0 || r.Off+memLen > len(r.B) {
		return snapState{}, nil, fmt.Errorf("gameabi: snapshot: truncated linear memory (want %d)", memLen)
	}
	linmem := r.B[r.Off : r.Off+memLen]
	return st, linmem, nil
}

// ---- zstd ----------------------------------------------------------------------

// One process-wide encoder/decoder pair: both are safe for concurrent use by
// the stateless EncodeAll/DecodeAll calls.
var (
	zstdEnc, _ = zstd.NewWriter(nil)
	zstdDec, _ = zstd.NewReader(nil)
)

func zstdEncode(b []byte) []byte { return zstdEnc.EncodeAll(b, nil) }
func zstdDecode(b []byte) ([]byte, error) {
	return zstdDec.DecodeAll(b, nil)
}
