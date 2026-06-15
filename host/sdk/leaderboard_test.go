package sdk

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestResolveLeaderboardSpecDefaults(t *testing.T) {
	got := ResolveLeaderboardSpec(nil)
	if got.Direction != HigherBetter || got.Aggregation != BestResult || got.Format != Integer {
		t.Fatalf("nil spec = %+v, want best/higher/integer", got)
	}
	if got.MetricLabel == "" {
		t.Fatalf("nil spec has empty label")
	}
	// Explicit label is kept; empty label falls back to the default.
	g2 := ResolveLeaderboardSpec(&LeaderboardSpec{MetricLabel: "Chips", Aggregation: CumulativeSum})
	if g2.MetricLabel != "Chips" || g2.Aggregation != CumulativeSum {
		t.Fatalf("explicit spec lost: %+v", g2)
	}
}

func TestRankStandingsOrderTiesAndExclusion(t *testing.T) {
	t0 := time.Unix(1000, 0)
	scores := []Score{
		{AccountID: "a", Value: 50, Achieved: t0},
		{AccountID: "b", Value: 80, Achieved: t0},
		{AccountID: "c", Value: 80, Achieved: t0.Add(-time.Minute)}, // same value, earlier -> ranks ahead of b
		{AccountID: "gone", Value: 999, Achieved: t0},               // not live -> excluded
	}
	resolve := func(id string) (string, bool) {
		if id == "gone" {
			return "", false
		}
		return "H-" + id, true
	}
	rows := RankStandings(scores, HigherBetter, resolve, 0, 0)
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3 (excluded one)", len(rows))
	}
	if rows[0].AccountID != "c" || rows[1].AccountID != "b" || rows[2].AccountID != "a" {
		t.Fatalf("order=%v want c,b,a", []string{rows[0].AccountID, rows[1].AccountID, rows[2].AccountID})
	}
	if rows[0].Rank != 1 || rows[2].Rank != 3 {
		t.Fatalf("ranks not assigned: %+v", rows)
	}
	if rows[0].Handle != "H-c" {
		t.Fatalf("handle=%q want H-c", rows[0].Handle)
	}

	// Lower-better flips order; paging keeps true ranks.
	low := RankStandings(scores, LowerBetter, resolve, 1, 1)
	if len(low) != 1 || low[0].AccountID != "c" || low[0].Rank != 2 {
		t.Fatalf("lower-better page=%+v want c at rank 2", low)
	}
}

func TestWindowStartUTC(t *testing.T) {
	// 2026-06-03 is a Wednesday.
	now := time.Date(2026, 6, 3, 15, 30, 0, 0, time.UTC)
	if _, bounded := WindowStart(AllTime, now); bounded {
		t.Fatalf("all-time should be unbounded")
	}
	day, _ := WindowStart(Daily, now)
	if !day.Equal(time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily start=%v want UTC midnight", day)
	}
	week, _ := WindowStart(Weekly, now)
	if !week.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) { // Monday
		t.Fatalf("weekly start=%v want Monday 2026-06-01", week)
	}
}

// ---- cached reader -------------------------------------------------------------

// countingReaderFake is a LeaderboardReader whose full-board fetches are
// counted, so the cache tests can assert exactly how many hit the backend.
type countingReaderFake struct {
	mu    sync.Mutex
	calls int
	rows  []Standing
	err   error
}

func (f *countingReaderFake) Spec(string) LeaderboardSpec { return DefaultLeaderboardSpec }

func (f *countingReaderFake) Standings(_ context.Context, _ string, _ Window, limit, offset int) ([]Standing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return pageStandings(f.rows, limit, offset), nil
}

func (f *countingReaderFake) PlayerStanding(context.Context, string, string, Window) (Standing, bool, error) {
	panic("cached reader must serve PlayerStanding from the cached board")
}

func (f *countingReaderFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func cachedWithClock(inner LeaderboardReader, ttl time.Duration, now *time.Time) *cachedReader {
	c := NewCachedReader(inner, ttl).(*cachedReader)
	c.now = func() time.Time { return *now }
	return c
}

// One backend aggregation serves every page flip, the PlayerStanding rank
// lookup, and repeat views within the TTL; expiry refetches; distinct
// (slug, window) keys do not share entries.
func TestCachedReaderCollapsesReadsAndExpires(t *testing.T) {
	rows := []Standing{
		{Rank: 1, AccountID: "b", Handle: "B", Value: 80},
		{Rank: 2, AccountID: "a", Handle: "A", Value: 50},
	}
	fake := &countingReaderFake{rows: rows}
	now := time.Unix(1000, 0)
	c := cachedWithClock(fake, 15*time.Second, &now)
	ctx := context.Background()

	// First read fetches; page flips + rank lookup + repeats all hit the cache.
	page, err := c.Standings(ctx, "g", AllTime, 1, 0)
	if err != nil || len(page) != 1 || page[0].AccountID != "b" {
		t.Fatalf("page=%+v err=%v want b", page, err)
	}
	page2, _ := c.Standings(ctx, "g", AllTime, 1, 1)
	if len(page2) != 1 || page2[0].AccountID != "a" || page2[0].Rank != 2 {
		t.Fatalf("page2=%+v want a at true rank 2", page2)
	}
	st, ok, err := c.PlayerStanding(ctx, "g", "a", AllTime)
	if err != nil || !ok || st.Rank != 2 || st.Value != 50 {
		t.Fatalf("standing=%+v ok=%v err=%v want a rank2/50", st, ok, err)
	}
	if _, ok, _ := c.PlayerStanding(ctx, "g", "nobody", AllTime); ok {
		t.Fatal("unknown account must have no standing")
	}
	if n := fake.count(); n != 1 {
		t.Fatalf("backend fetches=%d want 1 (cache collapses the rest)", n)
	}

	// A different window is its own key.
	if _, err := c.Standings(ctx, "g", Daily, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := fake.count(); n != 2 {
		t.Fatalf("backend fetches=%d want 2 (distinct window key)", n)
	}

	// Within the TTL nothing refetches; past it, one read refetches.
	now = now.Add(14 * time.Second)
	_, _ = c.Standings(ctx, "g", AllTime, 0, 0)
	if n := fake.count(); n != 2 {
		t.Fatalf("backend fetches=%d want 2 (still fresh)", n)
	}
	now = now.Add(2 * time.Second)
	_, _ = c.Standings(ctx, "g", AllTime, 0, 0)
	if n := fake.count(); n != 3 {
		t.Fatalf("backend fetches=%d want 3 (expired -> refetch)", n)
	}
}

// Errors are returned but never cached: the next read retries the backend.
func TestCachedReaderDoesNotCacheErrors(t *testing.T) {
	fake := &countingReaderFake{err: context.DeadlineExceeded}
	now := time.Unix(1000, 0)
	c := cachedWithClock(fake, 15*time.Second, &now)
	ctx := context.Background()

	if _, err := c.Standings(ctx, "g", AllTime, 0, 0); err == nil {
		t.Fatal("want backend error surfaced")
	}
	fake.mu.Lock()
	fake.err = nil
	fake.rows = []Standing{{Rank: 1, AccountID: "a", Handle: "A", Value: 1}}
	fake.mu.Unlock()
	got, err := c.Standings(ctx, "g", AllTime, 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%+v err=%v want recovered read", got, err)
	}
	if n := fake.count(); n != 2 {
		t.Fatalf("backend fetches=%d want 2 (error not cached)", n)
	}
}

// Concurrent misses on one key are single-flighted: every reader gets the
// board, the backend sees one fetch.
func TestCachedReaderSingleFlight(t *testing.T) {
	release := make(chan struct{})
	fake := &gateReaderFake{release: release, rows: []Standing{{Rank: 1, AccountID: "a", Handle: "A", Value: 9}}}
	now := time.Unix(1000, 0)
	c := cachedWithClock(fake, 15*time.Second, &now)

	const readers = 8
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := c.Standings(context.Background(), "g", AllTime, 0, 0)
			if err == nil && (len(rows) != 1 || rows[0].AccountID != "a") {
				err = context.Canceled // any sentinel: wrong rows
			}
			errs <- err
		}()
	}
	// Let the goroutines pile onto the one in-flight fetch, then release it.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("reader failed: %v", err)
		}
	}
	if n := fake.count(); n != 1 {
		t.Fatalf("backend fetches=%d want 1 (single-flight)", n)
	}
}

// gateReaderFake blocks every Standings call until release is closed, counting
// calls — the single-flight test's controllable slow backend.
type gateReaderFake struct {
	mu      sync.Mutex
	calls   int
	release chan struct{}
	rows    []Standing
}

func (f *gateReaderFake) Spec(string) LeaderboardSpec { return DefaultLeaderboardSpec }

func (f *gateReaderFake) Standings(context.Context, string, Window, int, int) ([]Standing, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	<-f.release
	return f.rows, nil
}

func (f *gateReaderFake) PlayerStanding(context.Context, string, string, Window) (Standing, bool, error) {
	return Standing{}, false, nil
}

func (f *gateReaderFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ttl <= 0 disables caching entirely (the inner reader is returned as-is).
func TestCachedReaderZeroTTLPassthrough(t *testing.T) {
	fake := &countingReaderFake{}
	if r := NewCachedReader(fake, 0); r != LeaderboardReader(fake) {
		t.Fatal("ttl<=0 must return the inner reader unchanged")
	}
}
