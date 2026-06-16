package gameabi

import (
	"context"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/blobstore"
	"github.com/shellcade/kit/v2/host/sdk"
)

// TestD4CheckpointCost measures the worst-case per-room checkpoint cost
// (serialize + MAC + write) against the in-memory blobstore double, so the
// rooms-per-peer cap can be derived for the 60s drain budget (spike D.4, design
// D5). It is the "current checkpoint path as proxy" measurement: SnapshotHandler
// (the deterministic codec) + Sealer.Seal (HMAC) + Store.Put. The in-memory
// store has zero network RTT, so the reported time is the CPU floor; production
// adds one Tigris PUT RTT per room (the cap must budget that separately).
//
// Run with: go test ./internal/gameabi/ -run TestD4CheckpointCost -v
func TestD4CheckpointCost(t *testing.T) {
	g := loadFixture(t, Options{}).(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 12345, SeedSet: true}
	roster := []sdk.Player{p1}
	start := time.Unix(1_700_000_000, 0)
	ctx := context.Background()

	// Drive the fixture to a realistic mid-game state (instantiate, join, wakes,
	// entropy draws, input context) so the captured memory is representative.
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	r := newReplayRoom(roster, cfg, start)
	h.OnStart(r)
	h.OnJoin(r, p1)
	for i := 0; i < 8; i++ {
		r.clock = r.clock.Add(50 * time.Millisecond)
		h.OnTick(r, r.clock)
	}
	h.OnInput(r, p1, runeIn('r'))
	h.OnInput(r, p1, runeIn('i'))

	cs := NewCheckpointStore(blobstore.NewMemory(), blobstore.NewHMACSealer([]byte("server-side-key")))

	// Measure the serialized blob size once.
	blob, err := SnapshotHandler(h)
	if err != nil {
		t.Fatalf("SnapshotHandler: %v", err)
	}

	// Time N sequential checkpoints (each at a fresh epoch) and report the worst
	// and mean per-room cost. Sequential because a single room is serialized per
	// actor; the drain runs many rooms in parallel, so the per-room cost is what
	// multiplies against the rooms-per-peer cap (÷ parallelism).
	const n = 200
	var worst time.Duration
	var total time.Duration
	for epoch := int64(0); epoch < n; epoch++ {
		t0 := time.Now()
		if err := CheckpointHandler(ctx, cs, "0190b8a0-1234-7abc-8def-0123456789ab", epoch, h); err != nil {
			t.Fatalf("CheckpointHandler epoch %d: %v", epoch, err)
		}
		d := time.Since(t0)
		total += d
		if d > worst {
			worst = d
		}
	}
	mean := total / n

	t.Logf("D.4 per-room checkpoint cost (in-memory blobstore, fixture mid-game):")
	t.Logf("  snapshot blob size = %d bytes (compressed)", len(blob))
	t.Logf("  iterations         = %d", n)
	t.Logf("  mean per room      = %s", mean)
	t.Logf("  worst per room     = %s", worst)
	t.Logf("  (CPU floor only; production adds ~1 Tigris PUT RTT per room)")

	// The measured cost above is INFORMATIONAL — the headline (mean/worst) is
	// reported via t.Log and recorded in the change docs (design D.4); it is NOT a
	// CI gate. A wall-clock CPU-floor assert is environment-sensitive: a shared CI
	// runner measured 84ms against a former 50ms bound and flaked the suite.
	// Measurement tests must not gate CI on wall-clock, so this is only a wildly
	// generous sanity bound that fires solely on a genuine hang/pathology, never on
	// a loaded box.
	if worst > 5*time.Second {
		t.Fatalf("worst per-room checkpoint %s is pathological (sanity bound only; the number is informational)", worst)
	}
}

// BenchmarkD4Checkpoint is the same serialize+MAC+write path as a Go benchmark,
// for `go test -bench BenchmarkD4Checkpoint -benchmem`.
func BenchmarkD4Checkpoint(b *testing.B) {
	g, err := LoadGame(fixturePath, Options{})
	if err != nil {
		b.Fatalf("LoadGame: %v", err)
	}
	wg := g.(*wasmGame)
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 12345, SeedSet: true}
	roster := []sdk.Player{p1}
	start := time.Unix(1_700_000_000, 0)
	h := wg.NewRoom(cfg, sdk.Services{Log: quietLog()}).(*wasmHandler)
	r := newReplayRoom(roster, cfg, start)
	h.OnStart(r)
	h.OnJoin(r, p1)
	for i := 0; i < 8; i++ {
		r.clock = r.clock.Add(50 * time.Millisecond)
		h.OnTick(r, r.clock)
	}
	cs := NewCheckpointStore(blobstore.NewMemory(), blobstore.NewHMACSealer([]byte("k")))
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := CheckpointHandler(ctx, cs, "bench-room", int64(i), h); err != nil {
			b.Fatalf("CheckpointHandler: %v", err)
		}
	}
}
