package gameabi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/blobstore"
)

// fakeClock is a controllable Clock for the cadence tests: Now is settable and
// After hands back a channel the test fires by advancing time. Only one pending
// timer at a time is needed (the scheduler arms one interval, waits, re-arms).
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.pending = append(c.pending, t)
	return t.ch
}

// advance moves time forward, firing every timer due at or before the new now.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var stay []*fakeTimer
	var fire []*fakeTimer
	for _, t := range c.pending {
		if !t.at.After(now) {
			fire = append(fire, t)
		} else {
			stay = append(stay, t)
		}
	}
	c.pending = stay
	c.mu.Unlock()
	for _, t := range fire {
		t.ch <- now
	}
}

// armed reports how many timers are currently waiting (the scheduler arms one
// per interval; the test waits for it before advancing to avoid a race).
func (c *fakeClock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func waitArmed(t *testing.T, c *fakeClock) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if c.armed() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduler never armed a timer")
}

// The scheduler fires checkpoints at base ± jitter, hands each a monotonic epoch
// starting at 0, and stops cleanly on Close.
func TestCadenceFiresWithMonotonicEpochs(t *testing.T) {
	clk := newFakeClock()
	var mu sync.Mutex
	var epochs []int64
	fired := make(chan struct{}, 16)

	sch := NewCheckpointScheduler(CadenceConfig{
		Base:   30 * time.Second,
		Jitter: 5 * time.Second,
		Clock:  clk,
		Idle:   func() bool { return false },
		Checkpoint: func(ctx context.Context, epoch int64) error {
			mu.Lock()
			epochs = append(epochs, epoch)
			mu.Unlock()
			fired <- struct{}{}
			return nil
		},
	})
	go sch.Run(context.Background())

	for i := 0; i < 3; i++ {
		waitArmed(t, clk)
		clk.advance(35 * time.Second) // past base+jitter so the timer is due
		<-fired
	}
	sch.Close()

	mu.Lock()
	got := append([]int64(nil), epochs...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("fired %d times, want 3: %v", len(got), got)
	}
	for i, e := range got {
		if e != int64(i) {
			t.Fatalf("epoch[%d] = %d, want %d (monotonic from 0)", i, e, i)
		}
	}
}

// A scheduler seeded with StartEpoch fires its first checkpoint at that epoch and
// advances from there — the post-reclaim path where periodic checkpoints must
// supersede the drain blob (a strictly-monotonic pointer-epoch across restarts).
// NextEpoch reports the epoch the scheduler would use next (drain reads it).
func TestCadenceStartEpochSeeds(t *testing.T) {
	clk := newFakeClock()
	var mu sync.Mutex
	var epochs []int64
	fired := make(chan struct{}, 16)

	sch := NewCheckpointScheduler(CadenceConfig{
		Base:       30 * time.Second,
		Clock:      clk,
		StartEpoch: 100,
		Checkpoint: func(ctx context.Context, epoch int64) error {
			mu.Lock()
			epochs = append(epochs, epoch)
			mu.Unlock()
			fired <- struct{}{}
			return nil
		},
	})
	if got := sch.NextEpoch(); got != 100 {
		t.Fatalf("NextEpoch before any tick = %d, want the seed 100", got)
	}
	go sch.Run(context.Background())
	for i := 0; i < 2; i++ {
		waitArmed(t, clk)
		clk.advance(31 * time.Second)
		<-fired
	}
	sch.Close()

	mu.Lock()
	got := append([]int64(nil), epochs...)
	mu.Unlock()
	if len(got) != 2 || got[0] != 100 || got[1] != 101 {
		t.Fatalf("seeded epochs = %v, want [100 101]", got)
	}
	if ne := sch.NextEpoch(); ne != 102 {
		t.Fatalf("NextEpoch after 2 ticks = %d, want 102", ne)
	}
}

// Close BLOCKS until an in-flight fire completes (and the epoch it advanced is
// visible): the drain's close-then-NextEpoch sequence then truly fences the
// cadence, so the drain epoch is strictly above every committed periodic write.
func TestCadenceCloseWaitsForInFlightFire(t *testing.T) {
	clk := newFakeClock()
	enter := make(chan struct{}) // closed when fire() has started
	release := make(chan struct{})
	var enterOnce sync.Once

	sch := NewCheckpointScheduler(CadenceConfig{
		Base:  30 * time.Second,
		Clock: clk,
		Checkpoint: func(ctx context.Context, epoch int64) error {
			enterOnce.Do(func() { close(enter) })
			<-release // hold the fire open until the test releases it
			return nil
		},
	})
	go sch.Run(context.Background())

	waitArmed(t, clk)
	clk.advance(31 * time.Second)
	<-enter // a fire is now in flight, blocked on release

	// Close concurrently: it must NOT return while the fire is held.
	closed := make(chan struct{})
	go func() { sch.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a fire was still in flight")
	case <-time.After(100 * time.Millisecond):
		// good: Close is blocked waiting for the in-flight fire
	}
	if ne := sch.NextEpoch(); ne != 0 {
		t.Fatalf("NextEpoch mid-fire = %d, want 0 (not yet advanced)", ne)
	}

	close(release) // let the fire complete
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the in-flight fire completed")
	}
	if ne := sch.NextEpoch(); ne != 1 {
		t.Fatalf("NextEpoch after Close = %d, want 1 (the completed fire's advance is visible)", ne)
	}
}

// A drain (close-then-NextEpoch-then-Write) NEVER collides with the scheduler's
// periodic writes against a real CheckpointStore: after Close fences the cadence,
// NextEpoch is strictly above every committed periodic epoch, so the drain Write
// succeeds (no never-overwrite error) and advances the latest pointer. This is the
// peer drain sequence in miniature.
func TestCadenceDrainNeverCollides(t *testing.T) {
	clk := newFakeClock()
	cs := NewCheckpointStore(blobstore.NewMemory(), blobstore.NewHMACSealer([]byte("k")))
	const room = "0190b8a0-dead-7abc-8def-0123456789ab"
	payload := []byte("snapshot-bytes")
	fired := make(chan struct{}, 16)

	sch := NewCheckpointScheduler(CadenceConfig{
		Base:  30 * time.Second,
		Clock: clk,
		Checkpoint: func(ctx context.Context, epoch int64) error {
			err := cs.Write(ctx, room, epoch, payload)
			fired <- struct{}{}
			return err
		},
	})
	go sch.Run(context.Background())

	// Commit two periodic checkpoints (epochs 0, 1).
	for i := 0; i < 2; i++ {
		waitArmed(t, clk)
		clk.advance(31 * time.Second)
		<-fired
	}

	// Drain: fence the cadence, then write at NextEpoch.
	sch.Close()
	drainEpoch := sch.NextEpoch()
	if drainEpoch != 2 {
		t.Fatalf("drain epoch = %d, want 2 (strictly above committed periodic 0,1)", drainEpoch)
	}
	if err := cs.Write(context.Background(), room, drainEpoch, payload); err != nil {
		t.Fatalf("drain Write at epoch %d collided/failed: %v", drainEpoch, err)
	}
	if _, ep, err := cs.ReadLatest(context.Background(), room); err != nil || ep != drainEpoch {
		t.Fatalf("latest pointer after drain = (epoch %d, err %v), want epoch %d", ep, err, drainEpoch)
	}
}

// Intervals stay within [base-jitter, base+jitter].
func TestCadenceJitterBounds(t *testing.T) {
	clk := newFakeClock()
	const base = 30 * time.Second
	const jitter = 5 * time.Second
	var mu sync.Mutex
	var intervals []time.Duration

	var sch *CheckpointScheduler
	sch = NewCheckpointScheduler(CadenceConfig{
		Base:   base,
		Jitter: jitter,
		Clock:  clk,
		Idle:   func() bool { return false },
		Checkpoint: func(ctx context.Context, epoch int64) error {
			mu.Lock()
			intervals = append(intervals, sch.lastInterval())
			mu.Unlock()
			return nil
		},
	})
	go sch.Run(context.Background())

	for i := 0; i < 50; i++ {
		waitArmed(t, clk)
		clk.advance(base + jitter) // always enough to fire
		// brief settle so the callback records before the next arm
		for sch.lastInterval() == 0 && i == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(time.Millisecond)
	}
	sch.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(intervals) < 10 {
		t.Fatalf("only %d intervals recorded", len(intervals))
	}
	sawBelow, sawAbove := false, false
	for _, d := range intervals {
		if d < base-jitter || d > base+jitter {
			t.Fatalf("interval %v out of [%v, %v]", d, base-jitter, base+jitter)
		}
		if d < base {
			sawBelow = true
		}
		if d > base {
			sawAbove = true
		}
	}
	if !sawBelow || !sawAbove {
		t.Errorf("jitter not exercised both directions (below=%v above=%v)", sawBelow, sawAbove)
	}
}

// While the idle probe reports true, the scheduler arms its timer but does NOT
// fire a checkpoint (lobby-idle rooms are suppressed); epochs do not advance.
func TestCadenceSkipsWhileIdle(t *testing.T) {
	clk := newFakeClock()
	var idle struct {
		mu sync.Mutex
		v  bool
	}
	idle.v = true
	fired := make(chan int64, 16)

	sch := NewCheckpointScheduler(CadenceConfig{
		Base:   30 * time.Second,
		Jitter: 0,
		Clock:  clk,
		Idle: func() bool {
			idle.mu.Lock()
			defer idle.mu.Unlock()
			return idle.v
		},
		Checkpoint: func(ctx context.Context, epoch int64) error {
			fired <- epoch
			return nil
		},
	})
	go sch.Run(context.Background())

	// Two idle intervals: timer arms and elapses, but no checkpoint fires.
	for i := 0; i < 2; i++ {
		waitArmed(t, clk)
		clk.advance(30 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case e := <-fired:
		t.Fatalf("idle room checkpointed at epoch %d", e)
	default:
	}

	// Become active; the next interval fires at epoch 0 (epochs never advanced
	// while idle).
	idle.mu.Lock()
	idle.v = false
	idle.mu.Unlock()
	waitArmed(t, clk)
	clk.advance(30 * time.Second)
	select {
	case e := <-fired:
		if e != 0 {
			t.Fatalf("first active checkpoint epoch = %d, want 0", e)
		}
	case <-time.After(time.Second):
		t.Fatal("active room never checkpointed")
	}
	sch.Close()
}

// Close stops the scheduler: Run returns and no further timers arm.
func TestCadenceStopsOnClose(t *testing.T) {
	clk := newFakeClock()
	done := make(chan struct{})
	sch := NewCheckpointScheduler(CadenceConfig{
		Base:       30 * time.Second,
		Clock:      clk,
		Idle:       func() bool { return false },
		Checkpoint: func(context.Context, int64) error { return nil },
	})
	go func() { sch.Run(context.Background()); close(done) }()
	waitArmed(t, clk)
	sch.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after Close")
	}
	// Close is idempotent.
	sch.Close()
}

// A cancelled context stops the scheduler just like Close.
func TestCadenceStopsOnContextCancel(t *testing.T) {
	clk := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sch := NewCheckpointScheduler(CadenceConfig{
		Base:       30 * time.Second,
		Clock:      clk,
		Idle:       func() bool { return false },
		Checkpoint: func(context.Context, int64) error { return nil },
	})
	go func() { sch.Run(ctx); close(done) }()
	waitArmed(t, clk)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
