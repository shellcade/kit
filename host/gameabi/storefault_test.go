package gameabi

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// These tests pin the slow-store → deadline-kill attribution path: a kv host
// call whose store outlives the callback kill switch must (a) release the room
// actor AT the deadline (the store context is derived from the callback ctx,
// not detached), and (b) settle the room WITHOUT a fault — host I/O slowness
// (a shared-Postgres brownout) must never feed quarantine. A genuine
// spin-to-deadline still faults. Store ERRORS must be logged host-side with
// slug/account/key instead of silently conflating into key-absent.
//
// The doubles are wired the only way a real game reaches storage: a fake
// sdk.AccountStore via sdk.Services.Accounts (AccountStore → Account.Store()).

// fakeAccounts yields accounts whose KVStore is the injected double.
type fakeAccounts struct{ kv sdk.KVStore }

func (f fakeAccounts) For(p sdk.Player) sdk.Account { return fakeAccount{p: p, kv: f.kv} }

type fakeAccount struct {
	p  sdk.Player
	kv sdk.KVStore
}

func (a fakeAccount) ID() string         { return a.p.AccountID }
func (a fakeAccount) Handle() string     { return a.p.Handle }
func (a fakeAccount) Kind() sdk.Kind     { return a.p.Kind }
func (a fakeAccount) Store() sdk.KVStore { return a.kv }

// slowKV blocks every operation for d or until the caller's context dies —
// exactly how a ctx-honoring Postgres driver behaves during a brownout.
type slowKV struct{ d time.Duration }

func (s slowKV) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.d):
		return nil
	}
}

func (s slowKV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := s.wait(ctx); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func (s slowKV) Set(ctx context.Context, key string, value []byte, rule sdk.MergeRule) error {
	return s.wait(ctx)
}

func (s slowKV) Delete(ctx context.Context, key string) error { return s.wait(ctx) }

// errKV fails every operation immediately (a DB refusing connections).
type errKV struct{ err error }

func (e errKV) Get(ctx context.Context, key string) ([]byte, bool, error) { return nil, false, e.err }
func (e errKV) Set(ctx context.Context, key string, value []byte, rule sdk.MergeRule) error {
	return e.err
}
func (e errKV) Delete(ctx context.Context, key string) error { return e.err }

// attrCapture is a slog.Handler recording message + attrs, so a test can
// assert the host logged a kv failure WITH its slug/account/key context.
type attrCapture struct {
	mu   sync.Mutex
	recs []capturedRec
}

type capturedRec struct {
	msg   string
	attrs map[string]string
}

func (c *attrCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *attrCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	c.mu.Lock()
	c.recs = append(c.recs, capturedRec{msg: r.Message, attrs: attrs})
	c.mu.Unlock()
	return nil
}
func (c *attrCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *attrCapture) WithGroup(string) slog.Handler      { return c }

func (c *attrCapture) find(msg string) (capturedRec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.msg == msg {
			return r, true
		}
	}
	return capturedRec{}, false
}

func (c *attrCapture) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.recs {
		if r.msg == msg {
			n++
		}
	}
	return n
}

// TestSlowStoreDeadlineIsNotAGameFault: the fixture's 'k' command does a kv set
// on the sender's store; the store stalls far past the 50ms callback deadline.
// The room actor must come back at the deadline (NOT after the store's full
// latency, and NOT after the detached 2s kvTimeout the old code used), the room
// must settle (wazero condemned the instance at the deadline), and NO fault may
// be booked — quarantine must never count a Postgres brownout against the game.
func TestSlowStoreDeadlineIsNotAGameFault(t *testing.T) {
	var faults atomic.Int32
	rec := newRecordingMetrics()
	g := loadFixture(t, Options{
		CallbackDeadline: 50 * time.Millisecond,
		OnFault:          func(string) { faults.Add(1) },
		Metrics:          rec,
	})
	svc := sdk.Services{Log: quietLog(), Accounts: fakeAccounts{kv: slowKV{d: 30 * time.Second}}}
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, svc)
	tr.Start()
	tr.Join(p1)

	start := time.Now()
	tr.Input(p1, runeIn('k')) // guest blocks in the host's own kv_set
	elapsed := time.Since(start)

	// The actor was released by the kill switch: well under the old detached
	// 2s kvTimeout (the bound is ~50ms; 1s leaves generous CI slack).
	if elapsed >= time.Second {
		t.Fatalf("actor blocked %v in a slow kv host call, want release at the ~50ms deadline", elapsed)
	}
	if !tr.Ended {
		t.Fatal("room did not settle after the deadline condemned the instance")
	}
	if n := faults.Load(); n != 0 {
		t.Fatalf("host-I/O deadline booked %d fault(s) — DB slowness would feed quarantine", n)
	}
	if _, _, f := rec.snapshot(); len(f) != 0 {
		t.Fatalf("fault metric moved on a host-I/O deadline: %v", f)
	}
	if got := rec.totalHostIODeadlines(); got != 1 {
		t.Fatalf("host-I/O deadline metric = %d, want 1", got)
	}
	if got := rec.totalDeadlines(); got != 0 {
		t.Fatalf("spin-to-deadline metric = %d, want 0 (this was host I/O, not a spin)", got)
	}
}

// TestErroringStoreIsLoggedNotFatal: a store that ERRORS (DB down, not slow)
// must not kill the room or fault the game — the guest sees the ABI's silent
// key-absent/dropped-write result — but the host must log each failure with
// slug/account/key (the old code discarded kv_set errors entirely) and count it.
func TestErroringStoreIsLoggedNotFatal(t *testing.T) {
	var faults atomic.Int32
	rec := newRecordingMetrics()
	g := loadFixture(t, Options{
		OnFault: func(string) { faults.Add(1) },
		Metrics: rec,
	})
	logCap := &attrCapture{}
	svc := sdk.Services{Log: slog.New(logCap), Accounts: fakeAccounts{kv: errKV{err: errors.New("pg: connection refused")}}}
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, svc)
	tr.Start()
	tr.Join(p1)

	tr.Input(p1, runeIn('k')) // guest kv set + get, both erroring

	if tr.Ended {
		t.Fatal("room settled on a store ERROR — an erroring DB must not kill rooms")
	}
	if n := faults.Load(); n != 0 {
		t.Fatalf("store error booked %d fault(s)", n)
	}
	for _, op := range []string{"kv_set", "kv_get"} {
		r, ok := logCap.find("gameabi: " + op + " failed")
		if !ok {
			t.Fatalf("no host-side log for the failed %s; got %+v", op, logCap.recs)
		}
		if r.attrs["slug"] != "fixture" || r.attrs["account"] != p1.AccountID || r.attrs["key"] != "visits" {
			t.Fatalf("%s failure logged without slug/account/key context: %+v", op, r.attrs)
		}
	}
	if got := rec.totalKVErrors(); got != 2 {
		t.Fatalf("kv error metric = %d, want 2 (set + get)", got)
	}
}

// TestSpinToDeadlineStillFaults guards the discrimination from the other side:
// a guest that genuinely burns its budget ('l' spins forever) is still a fault
// feeding quarantine, even with a kv store wired — host-I/O exemption must not
// become quarantine evasion.
func TestSpinToDeadlineStillFaults(t *testing.T) {
	var faults atomic.Int32
	rec := newRecordingMetrics()
	g := loadFixture(t, Options{
		CallbackDeadline: 50 * time.Millisecond,
		OnFault:          func(string) { faults.Add(1) },
		Metrics:          rec,
	})
	svc := sdk.Services{Log: quietLog(), Accounts: fakeAccounts{kv: slowKV{d: time.Second}}}
	cfg := sdk.RoomConfig{Mode: sdk.ModeSolo, Capacity: 1, MinPlayers: 1, Seed: 7, SeedSet: true}
	tr := sdk.NewTestRoom(g, cfg, svc)
	tr.Start()
	tr.Join(p1)

	tr.Input(p1, runeIn('l')) // spin to the deadline

	if !tr.Ended {
		t.Fatal("room did not settle after a spin-to-deadline")
	}
	if n := faults.Load(); n != 1 {
		t.Fatalf("spin-to-deadline faults = %d, want 1", n)
	}
	if got := rec.totalDeadlines(); got != 1 {
		t.Fatalf("spin-to-deadline metric = %d, want 1", got)
	}
	if got := rec.totalHostIODeadlines(); got != 0 {
		t.Fatalf("host-I/O deadline metric = %d, want 0 (the guest spun; no host I/O expired)", got)
	}
}

// TestGuestLogCaps pins the guest log meters shared by BOTH guest log paths
// (stdout/stderr logWriter and the `log` host function): per-write truncation
// at guestLogMaxWrite, a per-window byte budget with exactly one rate-limited
// Warn marker, and budget recovery in the next window.
func TestGuestLogCaps(t *testing.T) {
	logCap := &attrCapture{}
	h := &wasmHandler{
		game: &wasmGame{meta: sdk.GameMeta{Slug: "fixture"}},
		svc:  sdk.Services{Log: slog.New(logCap)},
	}
	w := &logWriter{h: h}

	big := strings.Repeat("x", guestLogMaxWrite+1000)
	if n, err := w.Write([]byte(big)); n != len(big) || err != nil {
		t.Fatalf("Write = (%d, %v), want full length consumed", n, err)
	}
	r, ok := logCap.find("guest")
	if !ok {
		t.Fatal("guest stdout write produced no log record")
	}
	if got, want := len(r.attrs["out"]), len(truncateGuestLog(big)); got != want {
		t.Fatalf("guest write logged %d bytes, want truncated %d", got, want)
	}
	if !strings.HasSuffix(r.attrs["out"], "…[truncated]") {
		t.Fatal("oversized guest write not marked truncated")
	}

	// Exhaust the window budget: emission stops, ONE Warn marker fires.
	for i := 0; i < guestLogBudget/guestLogMaxWrite+3; i++ {
		_, _ = w.Write([]byte(big))
	}
	if got := logCap.count("gameabi: guest log output rate-limited"); got != 1 {
		t.Fatalf("rate-limited markers = %d, want exactly 1 per window", got)
	}
	emitted := logCap.count("guest")
	if emitted > guestLogBudget/guestLogMaxWrite+1 {
		t.Fatalf("emitted %d guest records, want the budget to stop emission", emitted)
	}
	_, _ = w.Write([]byte("still over budget"))
	if got := logCap.count("guest"); got != emitted {
		t.Fatalf("write emitted past the exhausted budget (%d -> %d records)", emitted, got)
	}

	// A new window restores the budget (and re-arms the marker).
	h.logWindowStart = time.Now().Add(-2 * guestLogWindow)
	_, _ = w.Write([]byte("fresh window"))
	if got := logCap.count("guest"); got != emitted+1 {
		t.Fatalf("budget did not recover in a new window (%d -> %d records)", emitted, got)
	}
}
