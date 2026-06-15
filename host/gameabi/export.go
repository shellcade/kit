package gameabi

import (
	"context"
	"fmt"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// Exported entry points for out-of-package drivers (the conformance harness, and
// later the engine's hibernation path). They keep wasmGame/wasmHandler
// unexported while giving callers the snapshot/restore + memory-probe surface
// they need to instrument a real wasm room.

// SnapshotHandler freezes a wasm room handler into a portable blob. h must be a
// handler returned by a wasm game's NewRoom, taken at a quiescent point (no
// guest call on the stack).
func SnapshotHandler(h sdk.Handler) ([]byte, error) {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return nil, fmt.Errorf("gameabi: SnapshotHandler: handler is not a wasm room")
	}
	return wh.Snapshot()
}

// RestoreHandler rehydrates a blob into a fresh handler bound to game g. g must
// be the same artifact the blob was taken from (the embedded sha256 + ABI
// version are verified).
//
// The restored handler resumes the guest's linear memory, clock, roster, input
// context, RoomConfig, and entropy position — everything the snapshot owns. It
// does NOT resume the host SERVICES (leaderboard, per-user KV, config): those
// are live host resources, not part of the portable blob, so the caller must
// rebind them with BindServices before driving the first callback, exactly as
// the engine wires services into a fresh NewRoom. A restored room with no
// services no-ops kv/config/leaderboard host calls and will diverge from a live
// room that has them.
func RestoreHandler(g sdk.Game, blob []byte) (sdk.Handler, error) {
	wg, ok := g.(*wasmGame)
	if !ok {
		return nil, fmt.Errorf("gameabi: RestoreHandler: game is not a wasm game")
	}
	return wg.Restore(blob)
}

// CloseHandler releases a handler's live plugin instance WITHOUT driving the
// room — the disposal path for a restored handler that was never adopted by a
// runtime. RestoreHandler returns a handler holding a live instance with
// grown, written linear memory (up to the game's 32MiB cap); the instance is
// otherwise closed only via OnClose through a running room, so dropping an
// unadopted handler pins that memory in the compiled plugin's shared wazero
// runtime until process restart. Every Restore call site guards its error and
// lost-race returns with the adopted-flag pattern:
//
//	adopted := false
//	defer func() {
//		if !adopted {
//			gameabi.CloseHandler(h)
//		}
//	}()
//	...
//	ctl := sdk.NewRoomRuntime(roomID, h, ...) // the runtime owns h from here
//	adopted = true
//
// Safe on a never-driven handler (no guest call is on the stack, so the
// instance closes immediately) and idempotent. No-op (reports false) if h is
// not a wasm room.
func CloseHandler(h sdk.Handler) bool {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return false
	}
	wh.closeInstance()
	return true
}

// CheckpointHandler captures a NON-DESTRUCTIVE snapshot of a live wasm room and
// writes it through cs at the given epoch — the periodic-durability path
// (room-hosting spec "Periodic Room Checkpoints", design D5). It reuses the
// hibernation codec's deterministic snapshot byte-for-byte (SnapshotHandler);
// unlike sdk.Room.Hibernate it does NOT end or dispose the room, so the same
// handler keeps running and is checkpointed again at the next epoch. The capture
// MUST be taken at a quiescent point (no guest callback on the stack), exactly
// like SnapshotHandler — the actor schedules it on the room goroutine.
//
// NOTE this convenience runs capture AND store write on the calling goroutine;
// the production peer instead splits them (SnapshotHandler on the actor, Write
// from the scheduler/drain goroutine — peer.fireCheckpoint) so a slow store
// never stalls the room actor. Prefer the split anywhere a live room serves
// players; this composite remains for the conformance harness and tests.
func CheckpointHandler(ctx context.Context, cs *CheckpointStore, roomID string, epoch int64, h sdk.Handler) error {
	payload, err := SnapshotHandler(h)
	if err != nil {
		return fmt.Errorf("gameabi: checkpoint: capture: %w", err)
	}
	return cs.Write(ctx, roomID, epoch, payload)
}

// BindServices attaches live host services to a restored handler before it is
// driven. Services (leaderboard, per-user KV, config) are host resources that a
// snapshot deliberately does not carry; a resumed room must be rebound to the
// running instance's services so kv/config/leaderboard host calls behave as they
// did before hibernation. No-op (and reports false) if h is not a wasm room.
func BindServices(h sdk.Handler, svc sdk.Services) bool {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return false
	}
	wh.svc = svc
	// Re-apply the per-room host.* config overrides exactly as NewRoom does, so
	// a resumed room runs with the same admin-tuned cadence/deadline as a fresh
	// one (the snapshot deliberately carries neither services nor host config).
	if svc.Config != nil {
		if d, ok := readConfigDuration(svc.Config, cfgHeartbeatMS); ok {
			wh.heartbeat = clampDur(d, minHeartbeat, maxHeartbeat)
		}
		if d, ok := readConfigDuration(svc.Config, cfgDeadlineMS); ok {
			wh.deadline = clampDur(d, minDeadline, maxDeadline)
		}
	}
	return true
}

// HandlerConfig returns the wasm room's RoomConfig — for a restored handler
// that is the ORIGINAL room's config carried by the snapshot (mode, capacity,
// min players, seed), so a resume can rebuild the runtime with the room's real
// identity instead of synthesizing a fresh one.
func HandlerConfig(h sdk.Handler) (sdk.RoomConfig, bool) {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return sdk.RoomConfig{}, false
	}
	return wh.cfg, true
}

// GuestMemorySize reports the wasm room's current GUEST linear-memory size in
// bytes (the program's own TinyGo heap, not the extism runtime's). Returns 0 if
// h is not a wasm room or has no live instance. Sample it after a callback to
// track peak memory under a limit.
func GuestMemorySize(h sdk.Handler) uint32 {
	wh, ok := h.(*wasmHandler)
	if !ok || wh.inst == nil {
		return 0
	}
	mem := guestMemory(wh.inst)
	if mem == nil {
		return 0
	}
	return mem.Size()
}

// HandlerRoster returns the wasm room's last-seen roster — the same membership
// the snapshot codec records — so the hibernation header can carry it WITHOUT
// decompressing the blob. At an abandonment quiesce point the live room is
// empty, but the handler still holds the roster of the player(s) who were in it
// (the codec's roster of record), which is exactly who may resume the room.
// Returns nil if h is not a wasm room.
func HandlerRoster(h sdk.Handler) []sdk.Player {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return nil
	}
	return append([]sdk.Player(nil), wh.roster...)
}

// HandlerEnded reports whether the wasm room has settled (the guest called end,
// or a fault settled it).
func HandlerEnded(h sdk.Handler) bool {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return false
	}
	return wh.ended || wh.dead
}

// LastCallback reports the most recent callback's wasm exit code, any trap or
// deadline error, and whether that callback faulted the room (a non-zero exit or
// a kill-switch error settled it). A timed-out callback surfaces as faulted with
// a non-nil err — the harness names it against the per-callback deadline.
func LastCallback(h sdk.Handler) (exit uint32, err error, faulted bool) {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return 0, nil, false
	}
	return wh.lastExit, wh.lastErr, wh.lastErr != nil || wh.lastExit != 0
}

// HandlerDeadline returns the per-room callback deadline the handler enforces
// (after any host.* config override) — the harness names this as the limit when
// a callback breaches it.
func HandlerDeadline(h sdk.Handler) (d time.Duration, ok bool) {
	wh, k := h.(*wasmHandler)
	if !k {
		return 0, false
	}
	return wh.deadline, true
}

// CallbackSplit reports the cumulative wall time spent inside guest
// callbacks for h, and the portion of it spent in the send/identical HOST
// functions (delta apply + frame decode + fan-out) the guest invoked
// mid-callback. Guest-pure compute = total - host. Diagnostic surface for
// load benchmarks; actor-goroutine accuracy (read between callbacks).
func CallbackSplit(h sdk.Handler) (total, host time.Duration) {
	wh, ok := h.(*wasmHandler)
	if !ok {
		return 0, 0
	}
	return time.Duration(wh.cbTotalNanos), time.Duration(wh.cbHostNanos)
}

// MemoryCapBytes returns the wasm game's linear-memory cap in bytes (the
// load-time manifest MaxPages) — the harness names this as the memory limit.
func MemoryCapBytes(g sdk.Game) (uint64, bool) {
	wg, ok := g.(*wasmGame)
	if !ok {
		return 0, false
	}
	return uint64(wg.opts.MemoryPages) * 64 * 1024, true
}
