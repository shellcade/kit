package gameabi

import (
	"context"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/blobstore"
	"github.com/shellcade/kit/v2/host/sdk"
)

// CheckpointHandler captures the SAME deterministic snapshot the hibernation
// codec produces (no new payload format) and writes it through CheckpointStore
// WITHOUT ending the room: the handler keeps running afterward, and a Restore
// from the written checkpoint reproduces state exactly. This is the
// non-destructive periodic-durability path (distinct from the disposing
// Hibernate, which lives in internal/sdk and tears the room down).
func TestCheckpointHandlerNonDestructive(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 12345, SeedSet: true}
	roster := []sdk.Player{p1}
	start := time.Unix(1_700_000_000, 0)
	ctx := context.Background()

	cs := NewCheckpointStore(blobstore.NewMemory(), blobstore.NewHMACSealer([]byte("server-key")))
	const room = "0190b8a0-1234-7abc-8def-0123456789ab"

	// Drive a handler through a prefix, then CHECKPOINT it mid-life.
	prefix := func(h *wasmHandler) *replayRoom {
		r := newReplayRoom(roster, cfg, start)
		h.OnStart(r)
		h.OnJoin(r, p1)
		for i := 0; i < 4; i++ {
			r.clock = r.clock.Add(50 * time.Millisecond)
			h.OnTick(r, r.clock)
		}
		h.OnInput(r, p1, runeIn('r'))
		h.OnInput(r, p1, runeIn('i'))
		return r
	}

	// Control: prefix THEN continuation, never checkpointed.
	hCtl := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	rCtl := prefix(hCtl)
	checkpointClock := rCtl.clock
	rCtl.frames = nil
	snapScript(hCtl, rCtl, p1)
	wantFrames := rCtl.frames

	// Live handler: prefix, checkpoint, THEN keep driving the SAME handler.
	hLive := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	rLive := prefix(hLive)
	if err := CheckpointHandler(ctx, cs, room, 0, hLive); err != nil {
		t.Fatalf("CheckpointHandler: %v", err)
	}
	// Non-destructive: the room must NOT have ended, and must keep producing
	// the same continuation as the uninterrupted control.
	if HandlerEnded(hLive) {
		t.Fatal("checkpoint ended the room (must be non-destructive)")
	}
	rLive.frames = nil
	snapScript(hLive, rLive, p1)
	if len(rLive.frames) != len(wantFrames) {
		t.Fatalf("post-checkpoint continuation frame count %d, want %d", len(rLive.frames), len(wantFrames))
	}
	for i := range wantFrames {
		if !framesEqual(wantFrames[i], rLive.frames[i]) {
			t.Fatalf("post-checkpoint continuation frame %d diverged: room was disturbed by the checkpoint", i)
		}
	}

	// The written checkpoint restores to the SAME state, and a continuation from
	// the restored handler matches the control too (reuse of the codec, sealed).
	payload, epoch, err := cs.ReadLatest(ctx, room)
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if epoch != 0 {
		t.Errorf("epoch = %d, want 0", epoch)
	}
	hRestored, err := g.Restore(payload)
	if err != nil {
		t.Fatalf("Restore from checkpoint: %v", err)
	}
	rRestored := newReplayRoom(roster, cfg, checkpointClock)
	hRestored.OnResume(rRestored)
	snapScript(hRestored, rRestored, p1)
	if len(rRestored.frames) != len(wantFrames) {
		t.Fatalf("restored continuation frame count %d, want %d", len(rRestored.frames), len(wantFrames))
	}
	for i := range wantFrames {
		if !framesEqual(wantFrames[i], rRestored.frames[i]) {
			t.Fatalf("restored continuation frame %d diverged from control", i)
		}
	}
}

// CheckpointHandler refuses a handler that is not a wasm room.
func TestCheckpointHandlerNonWasm(t *testing.T) {
	cs := NewCheckpointStore(blobstore.NewMemory(), blobstore.NewHMACSealer([]byte("k")))
	if err := CheckpointHandler(context.Background(), cs, "room", 0, nil); err == nil {
		t.Fatal("CheckpointHandler(nil handler) should error")
	}
}
