package gameabi

import (
	"sync"
	"testing"

	"github.com/shellcade/kit/v2/host/sdk"
)

// recordingMetrics is a test double for the Options.Metrics surface.
type recordingMetrics struct {
	mu        sync.Mutex
	bytesOut  map[string]int
	bytesIn   map[string]int
	faults    map[string]int
	callbacks map[string]int   // slug/callback -> count
	deadlines map[string]int   // slug/callback -> count
	hostIO    map[string]int   // slug/callback -> count (host-I/O deadline kills)
	kvErrors  map[string]int   // slug/op -> count
	memBytes  map[string]int64 // slug -> summed linear-memory deltas (the gauge value)
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{bytesOut: map[string]int{}, bytesIn: map[string]int{}, faults: map[string]int{}, callbacks: map[string]int{}, deadlines: map[string]int{}, hostIO: map[string]int{}, kvErrors: map[string]int{}, memBytes: map[string]int64{}}
}

func (r *recordingMetrics) GameFrameBytesOut(slug string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bytesOut[slug] += n
}
func (r *recordingMetrics) GameInputBytesIn(slug string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bytesIn[slug] += n
}
func (r *recordingMetrics) GameFault(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.faults[slug] += 1
}
func (r *recordingMetrics) GameCallback(slug, callback string, seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callbacks[slug+"/"+callback]++
}
func (r *recordingMetrics) GameCallbackDeadline(slug, callback string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines[slug+"/"+callback]++
}
func (r *recordingMetrics) GameHostIODeadline(slug, callback string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostIO[slug+"/"+callback]++
}
func (r *recordingMetrics) GameKVError(slug, op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kvErrors[slug+"/"+op]++
}
func (r *recordingMetrics) GameLinearMemoryDelta(slug string, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memBytes[slug] += delta
}

func (r *recordingMetrics) memOf(slug string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.memBytes[slug]
}

func (r *recordingMetrics) snapshot() (out, in, faults map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := func(m map[string]int) map[string]int {
		c := make(map[string]int, len(m))
		for k, v := range m {
			c[k] = v
		}
		return c
	}
	return cp(r.bytesOut), cp(r.bytesIn), cp(r.faults)
}

func (r *recordingMetrics) totalCallbacks() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.callbacks {
		n += c
	}
	return n
}

func (r *recordingMetrics) totalHostIODeadlines() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.hostIO {
		n += c
	}
	return n
}

func (r *recordingMetrics) totalKVErrors() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.kvErrors {
		n += c
	}
	return n
}

func (r *recordingMetrics) totalDeadlines() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.deadlines {
		n += c
	}
	return n
}

// TestHostMeasuredCounters proves the byte counters move only from data the
// HOST moved across the module boundary: frames the host accepted (send /
// identical, logical frame size) and the normalized input payload it
// delivered — never module-reported figures.
func TestHostMeasuredCounters(t *testing.T) {
	rec := newRecordingMetrics()
	g := loadFixture(t, Options{Metrics: rec})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(p1)

	out, in, faults := rec.snapshot()
	if out["fixture"] <= 0 {
		t.Fatalf("frame bytes out after start+join = %d, want > 0 (fixture renders a frame)", out["fixture"])
	}
	if len(faults) != 0 {
		t.Fatalf("faults after clean start = %v, want none", faults)
	}
	if rec.totalCallbacks() == 0 {
		t.Fatal("no callback durations recorded after start+join")
	}
	if d := rec.totalDeadlines(); d != 0 {
		t.Fatalf("deadline kills after clean start = %d, want 0", d)
	}
	baseOut, baseIn := out["fixture"], in["fixture"]
	if baseIn != 0 {
		t.Fatalf("input bytes before any input = %d, want 0", baseIn)
	}

	// One input: the normalized payload is idx(4) + kind(1) + rune(4) + key(1).
	tr.Input(p1, runeIn('x'))
	out, in, _ = rec.snapshot()
	if got := in["fixture"]; got != 10 {
		t.Fatalf("input bytes after one input = %d, want 10", got)
	}
	if out["fixture"] <= baseOut {
		t.Fatalf("frame bytes did not grow after an input callback: %d -> %d", baseOut, out["fixture"])
	}
}

// TestLinearMemoryGauge proves the per-game linear-memory gauge is HOST-sampled
// from the actual wazero instance: a room's grown guest memory is folded in at
// birth, kept fresh on the heartbeat (OnTick — including the empty-room idle
// path, which still pins memory), summed across the game's rooms, and retired
// when an instance closes. The memory-pressure attribution series for a
// hoarding game that never faults.
func TestLinearMemoryGauge(t *testing.T) {
	rec := newRecordingMetrics()
	g := loadFixture(t, Options{Metrics: rec})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	svc := sdk.Services{Log: quietLog()}

	hA := g.NewRoom(cfg, svc).(*wasmHandler)
	trA := sdk.NewTestRoomFor(hA, cfg, svc)
	trA.Start()
	sizeA := int64(GuestMemorySize(hA))
	if sizeA <= 0 {
		t.Fatal("started room reports no guest linear memory")
	}
	if got := rec.memOf("fixture"); got != sizeA {
		t.Fatalf("gauge after start = %d, want the room's actual memory %d", got, sizeA)
	}

	// The heartbeat keeps the sample fresh even for an EMPTY room (no members
	// joined yet): OnTick skips the guest wake but still samples, and an
	// unchanged size reports no delta.
	trA.Tick()
	if got := rec.memOf("fixture"); got != int64(GuestMemorySize(hA)) {
		t.Fatalf("gauge after idle tick = %d, want %d", got, GuestMemorySize(hA))
	}

	// A second room of the same game adds its own contribution: the series is
	// the per-game SUM, not a last-writer-wins per-room value.
	hB := g.NewRoom(cfg, svc).(*wasmHandler)
	trB := sdk.NewTestRoomFor(hB, cfg, svc)
	trB.Start()
	trB.Join(p1)
	trB.Tick()
	sizeA, sizeB := int64(GuestMemorySize(hA)), int64(GuestMemorySize(hB))
	if got := rec.memOf("fixture"); got != sizeA+sizeB {
		t.Fatalf("gauge with two rooms = %d, want %d + %d", got, sizeA, sizeB)
	}

	// Closing a room's instance retires exactly its contribution.
	hB.OnClose(trB)
	if got := rec.memOf("fixture"); got != sizeA {
		t.Fatalf("gauge after closing one room = %d, want %d", got, sizeA)
	}
	hA.OnClose(trA)
	if got := rec.memOf("fixture"); got != 0 {
		t.Fatalf("gauge after closing all rooms = %d, want 0", got)
	}
}

// TestFaultCounter proves a guest trap reaches the metrics fault counter via
// the host's fault path (the same hook quarantine uses).
func TestFaultCounter(t *testing.T) {
	rec := newRecordingMetrics()
	g := loadFixture(t, Options{Metrics: rec})
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, sdk.Services{Log: quietLog()})
	tr.Start()
	tr.Join(p1)

	tr.Input(p1, runeIn('p')) // the fixture's panic command: a guest trap

	_, _, faults := rec.snapshot()
	if faults["fixture"] != 1 {
		t.Fatalf("faults after guest panic = %v, want fixture:1", faults)
	}
}
