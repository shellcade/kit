package gameabi

// BONEYARD Model A load spike: ONE room, a ramp of roster sizes (50 → 1000),
// a real WASM guest (testdata/loadspike — a BONEYARD-shaped roguelike: 12
// floors of 140x40, wandering monsters, per-player scrolling 80x24 viewports
// composed and Sent on every wake).
//
// Shape: Go benchmarks — one sub-benchmark per roster size, one b.N iteration
// = one 50ms tick cycle (input fan-in + Advance + Tick + frame drain), so
// -benchtime=200x measures 200 ticks per size. ns/op is the full tick cycle;
// custom metrics break it down:
//
//	tick_p50_ms / tick_p95_ms / tick_max_ms — guest wake latency alone
//	over_50ms / over_100ms                  — heartbeat-budget / production-
//	                                          callback-deadline breaches (count)
//	input_us                                — mean host->guest input delivery
//	wire_kb_tick                            — host-measured guest frame bytes
//	                                          out per tick (delta containers)
//	guest_mb / heap_mb                      — guest linear memory, host heap
//	snap_ms / snap_mb                       — hibernation snapshot encode+size
//	join_us                                 — mean per-player join cost (setup)
//
// Run:
//
//	go test ./internal/gameabi -bench LoadSpike -run '^$' -benchtime=200x -timeout 30m
//
// The callback deadline is raised to 5s so latency is MEASURED rather than
// killed; production-budget breaches are reported as counters instead.
import (
	"fmt"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// Two builds of the same guest: the production -gc=leaking profile (every
// allocation is permanent — fine for 8-player short-lived rooms, structurally
// incompatible with a resident room because the kit SDK's decodeCall decodes
// the FULL roster afresh on every callback), and TinyGo's default conservative
// GC (the candidate profile for resident rooms).
const (
	loadspikeLeakingPath = "testdata/loadspike/loadspike.wasm"
	loadspikeConsPath    = "testdata/loadspike/loadspike-cons.wasm"
)

// spikeMetrics implements Options.Metrics: host-measured byte counters (the
// host counts bytes it moved across the module boundary — a guest cannot
// inflate them). Atomics for safety; the TestRoom drive is single-goroutine.
type spikeMetrics struct {
	frameBytes atomic.Int64
	inputBytes atomic.Int64
	faults     atomic.Int64
}

func (m *spikeMetrics) GameFrameBytesOut(_ string, n int)       { m.frameBytes.Add(int64(n)) }
func (m *spikeMetrics) GameInputBytesIn(_ string, n int)        { m.inputBytes.Add(int64(n)) }
func (m *spikeMetrics) GameFault(string)                        { m.faults.Add(1) }
func (m *spikeMetrics) GameCallback(_, _ string, _ float64)     {}
func (m *spikeMetrics) GameCallbackDeadline(_, _ string)        {}
func (m *spikeMetrics) GameHostIODeadline(_, _ string)          {}
func (m *spikeMetrics) GameKVError(_, _ string)                 {}
func (m *spikeMetrics) GameLinearMemoryDelta(_ string, _ int64) {}

func BenchmarkLoadSpike(b *testing.B) {
	// The real scaling table: conservative GC, must complete every size.
	for _, n := range []int{50, 100, 250, 500, 1000} {
		b.Run(fmt.Sprintf("gc=cons/players=%d", n), func(b *testing.B) {
			runSpike(b, n, loadspikeConsPath, 2048, false)
		})
	}
	// The leak documentation runs: production -gc=leaking profile with a
	// 512 MiB cap; expected to OOM — survival is the metric, not a failure.
	for _, n := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("gc=leaking/players=%d", n), func(b *testing.B) {
			runSpike(b, n, loadspikeLeakingPath, 8192, true)
		})
	}
}

// runSpike drives one roster size. oomTolerant treats a guest fault as a
// measurement (oom_at_join / oom_at_tick metrics, early return) instead of a
// benchmark failure — used for the leaking-GC documentation runs.
func runSpike(b *testing.B, n int, path string, memPages uint32, oomTolerant bool) {
	m := &spikeMetrics{}
	g, err := LoadGame(path, Options{
		Heartbeat:        50 * time.Millisecond,
		MemoryPages:      memPages,
		CallbackDeadline: 5 * time.Second,
		Metrics:          m,
	})
	if err != nil {
		b.Fatalf("LoadGame(%s): %v", path, err)
	}

	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 1000, MinPlayers: 1, Seed: 42, SeedSet: true}
	svc := sdk.Services{Log: quietLog()}
	h := g.NewRoom(cfg, svc)
	tr := sdk.NewTestRoomFor(h, cfg, svc)
	tr.Start()

	players := make([]sdk.Player, n)
	for i := range players {
		players[i] = sdk.Player{
			AccountID: fmt.Sprintf("acct-%04d", i),
			Handle:    fmt.Sprintf("bot%04d", i),
			Kind:      sdk.KindMember,
			Conn:      fmt.Sprintf("conn-%04d", i),
		}
	}

	// ---- setup (untimed): join the roster, warm up, sanity-check rendering ----
	joinStart := time.Now()
	for i := range players {
		tr.Join(players[i])
		if exit, cerr, faulted := LastCallback(h); faulted {
			if oomTolerant {
				b.ReportMetric(float64(i+1), "oom_at_join")
				b.ReportMetric(float64(GuestMemorySize(h))/(1<<20), "guest_mb")
				return
			}
			b.Fatalf("join %d faulted (exit=%d err=%v)", i+1, exit, cerr)
		}
	}
	joinUs := float64(time.Since(joinStart).Microseconds()) / float64(n)

	const (
		tickStep    = 50 * time.Millisecond // the production heartbeat
		movesPerSec = 1.5                   // per-player input cadence (active play)
	)
	moves := []rune{'h', 'j', 'l', 'k'}
	inputsPerTick := int(float64(n) * movesPerSec * tickStep.Seconds())

	for w := 0; w < 5; w++ { // warmup: populate baselines, JIT paths, lazy allocs
		tr.Advance(tickStep)
		tr.Tick()
		if exit, cerr, faulted := LastCallback(h); faulted {
			if oomTolerant {
				b.ReportMetric(float64(w+1), "oom_at_warmup")
				b.ReportMetric(float64(GuestMemorySize(h))/(1<<20), "guest_mb")
				return
			}
			b.Fatalf("warmup %d faulted (exit=%d err=%v)", w, exit, cerr)
		}
	}
	if fr, ok := tr.LastFrame(players[0]); !ok {
		b.Fatal("no frame recorded for player 0 after warmup")
	} else {
		found := false
		for r := 0; r < 22 && !found; r++ {
			for c := 0; c < 80 && !found; c++ {
				found = fr.Cells[r][c].Rune == '@'
			}
		}
		if !found {
			b.Fatal("player 0's frame has no '@' — guest render broken")
		}
	}

	tickDur := make([]time.Duration, 0, b.N)
	var inputTotal time.Duration
	var inputCount int64
	over50, over100 := 0, 0
	m.frameBytes.Store(0)
	cbTotal0, cbHost0 := CallbackSplit(h)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Drain the previous tick's recorded frames, keeping backing arrays (a
		// real room hands one frame copy per player to a consumer channel;
		// accumulating history would skew host memory by gigabytes).
		for p, fs := range tr.Frames {
			tr.Frames[p] = fs[:0]
		}
		for j := 0; j < inputsPerTick; j++ {
			p := players[(i*inputsPerTick+j)%n]
			in := sdk.Input{Kind: sdk.InputRune, Rune: moves[(i+j)%len(moves)]}
			st := time.Now()
			tr.Input(p, in)
			inputTotal += time.Since(st)
			inputCount++
		}
		tr.Advance(tickStep)
		st := time.Now()
		tr.Tick()
		d := time.Since(st)
		tickDur = append(tickDur, d)
		if d > 50*time.Millisecond {
			over50++
		}
		if d > 100*time.Millisecond {
			over100++
		}
		if exit, cerr, faulted := LastCallback(h); faulted {
			if oomTolerant {
				b.StopTimer()
				b.ReportMetric(float64(i+1), "oom_at_tick")
				b.ReportMetric(float64(GuestMemorySize(h))/(1<<20), "guest_mb")
				return
			}
			b.Fatalf("tick %d faulted (exit=%d err=%v)", i, exit, cerr)
		}
	}
	b.StopTimer()

	// ---- custom metrics ----
	sort.Slice(tickDur, func(i, j int) bool { return tickDur[i] < tickDur[j] })
	msAt := func(q float64) float64 {
		idx := int(q * float64(len(tickDur)-1))
		return float64(tickDur[idx].Microseconds()) / 1000
	}
	b.ReportMetric(msAt(0.50), "tick_p50_ms")
	b.ReportMetric(msAt(0.95), "tick_p95_ms")
	b.ReportMetric(msAt(1.00), "tick_max_ms")
	b.ReportMetric(float64(over50), "over_50ms")
	b.ReportMetric(float64(over100), "over_100ms")
	if inputCount > 0 {
		b.ReportMetric(float64(inputTotal.Microseconds())/float64(inputCount), "input_us")
	}
	b.ReportMetric(float64(m.frameBytes.Load())/float64(b.N)/1024, "wire_kb_tick")
	// Guest vs host split: total guest-call wall time vs the portion inside
	// the send/identical host functions (delta apply + decode + fan-out).
	cbTotal1, cbHost1 := CallbackSplit(h)
	total := (cbTotal1 - cbTotal0).Seconds() * 1000 / float64(b.N)
	host := (cbHost1 - cbHost0).Seconds() * 1000 / float64(b.N)
	b.ReportMetric(total-host, "guest_ms_tick")
	b.ReportMetric(host, "host_ms_tick")
	b.ReportMetric(joinUs, "join_us")
	b.ReportMetric(float64(GuestMemorySize(h))/(1<<20), "guest_mb")

	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	b.ReportMetric(float64(ms.HeapInuse)/(1<<20), "heap_mb")

	snapStart := time.Now()
	blob, serr := SnapshotHandler(h)
	if serr != nil {
		b.Fatalf("snapshot failed: %v", serr)
	}
	b.ReportMetric(float64(time.Since(snapStart).Microseconds())/1000, "snap_ms")
	b.ReportMetric(float64(len(blob))/(1<<20), "snap_mb")
}
