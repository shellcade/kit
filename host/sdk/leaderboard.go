package sdk

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Direction is whether a higher or lower metric ranks better.
type Direction uint8

const (
	HigherBetter Direction = iota // default: bigger metric wins (WPM, chips)
	LowerBetter                   // smaller metric wins (time trials)
)

// Aggregation is how the default provider folds an account's own metrics.
type Aggregation uint8

const (
	BestResult    Aggregation = iota // default: best single metric (MAX/MIN by direction)
	CumulativeSum                    // sum of metrics
)

// MetricFormat is how the display layer renders a metric value.
type MetricFormat uint8

const (
	Integer  MetricFormat = iota // plain integer (default)
	Decimal1                     // value/10 with one decimal place
	Duration                     // seconds rendered as m:ss
)

// LeaderboardSpec is a game's optional declaration of how its board behaves. A
// nil *LeaderboardSpec on GameMeta means the defaults: best single result,
// higher is better, integer formatting. The spec carries no behavior of its own;
// the leaderboard service reads it to aggregate (default provider), order, and
// format that game's standings.
type LeaderboardSpec struct {
	MetricLabel string       `json:"metricLabel"` // column header, e.g. "WPM", "Chips", "Time"
	Direction   Direction    `json:"direction"`
	Aggregation Aggregation  `json:"aggregation"`
	Format      MetricFormat `json:"format"`
}

// DefaultLeaderboardSpec is the spec applied when a game declares none.
var DefaultLeaderboardSpec = LeaderboardSpec{
	MetricLabel: "Score",
	Direction:   HigherBetter,
	Aggregation: BestResult,
	Format:      Integer,
}

// ResolveLeaderboardSpec returns the effective spec for a possibly-nil
// declaration, applying the defaults for a nil spec.
func ResolveLeaderboardSpec(s *LeaderboardSpec) LeaderboardSpec {
	if s == nil {
		return DefaultLeaderboardSpec
	}
	out := *s
	if out.MetricLabel == "" {
		out.MetricLabel = DefaultLeaderboardSpec.MetricLabel
	}
	return out
}

// Window is a leaderboard time window.
type Window uint8

const (
	AllTime Window = iota
	Daily
	Weekly
)

// WindowStart returns the inclusive lower bound for a window computed in UTC,
// and whether the window is bounded at all. AllTime is unbounded. Daily starts
// at UTC midnight today; Weekly at Monday 00:00 UTC of the current week. No
// scheduled reset is involved — the boundary is derived from now at query time.
func WindowStart(w Window, now time.Time) (time.Time, bool) {
	u := now.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	switch w {
	case Daily:
		return day, true
	case Weekly:
		// Weekday(): Sunday=0..Saturday=6; days since Monday:
		offset := (int(u.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset), true
	default:
		return time.Time{}, false
	}
}

// Score is one account's value for a window, account-keyed and NOT ranked,
// carrying no handle. A LeaderboardProvider returns these; the platform resolves
// handles, excludes merged accounts, ranks, and pages.
type Score struct {
	AccountID string
	Value     int
	Achieved  time.Time
}

// Standing is one resolved, ranked leaderboard row for display.
type Standing struct {
	Rank      int
	AccountID string
	Handle    string // resolved live at read time
	Value     int
	Achieved  time.Time
}

// LeaderboardProvider produces a game's ranked values. A game may supply one to
// decide where/how its board's data is stored and computed; a game that supplies
// none uses the default provider that aggregates recorded results. A provider
// reads only its own game's data and MUST NOT rank or resolve handles.
type LeaderboardProvider interface {
	// Scores returns each account's value for the window (account-keyed,
	// unranked, handle-less).
	Scores(ctx context.Context, w Window) ([]Score, error)
}

// LeaderboardReader is the read side used by the lobby/UI (never handed to
// games). Implementations compose a game's LeaderboardProvider with platform
// identity resolution: handles are resolved live, merged/tombstoned accounts
// excluded, rows ordered by the spec's direction, ranked, and paged.
type LeaderboardReader interface {
	// Spec returns the resolved (nil-default-applied) spec for a game.
	Spec(slug string) LeaderboardSpec
	// Standings returns a ranked, handle-resolved page for a game + window.
	Standings(ctx context.Context, slug string, w Window, limit, offset int) ([]Standing, error)
	// PlayerStanding returns one account's rank + value for a game + window, or
	// ok=false when the account has no standing on that board.
	PlayerStanding(ctx context.Context, slug, accountID string, w Window) (Standing, bool, error)
}

// LeaderboardData is the read surface a provider/reader uses to reach durable
// data. Both the production Postgres store and the in-memory test store
// implement it, so the reader and the built-in providers are storage-agnostic.
type LeaderboardData interface {
	// ResultScores aggregates recorded results per account for a game + window
	// per the spec (the default provider uses this). Returns one Score per
	// account (Value = the aggregated metric, Achieved = when it was reached).
	ResultScores(ctx context.Context, slug string, spec LeaderboardSpec, w Window) ([]Score, error)
	// KVIntValues returns each account's integer value for (slug, key), used by
	// KV-backed providers such as the casino peak board.
	KVIntValues(ctx context.Context, slug, key string) ([]Score, error)
	// ResolveHandles maps account ids to their current live handle, OMITTING any
	// account that is merged-away/tombstoned or unknown (so it is excluded from
	// boards). The platform — not a provider — owns this.
	ResolveHandles(ctx context.Context, ids []string) (map[string]string, error)
}

// LeaderboardCustom is the OPTIONAL interface a Game implements to supply its own
// LeaderboardProvider (deciding where/how its board's values are stored and
// computed). A Game that does not implement it uses the default provider that
// aggregates recorded results. The provider is constructed with LeaderboardData
// so it can perform its own (own-game-scoped) reads.
type LeaderboardCustom interface {
	LeaderboardProvider(data LeaderboardData) LeaderboardProvider
}

// defaultProvider aggregates a game's recorded results per its spec.
type defaultProvider struct {
	data LeaderboardData
	slug string
	spec LeaderboardSpec
}

func (p *defaultProvider) Scores(ctx context.Context, w Window) ([]Score, error) {
	return p.data.ResultScores(ctx, p.slug, p.spec, w)
}

// reader is the generic LeaderboardReader composing a per-game provider with
// platform identity resolution. Shared by every factory.
type reader struct {
	data      LeaderboardData
	specs     map[string]LeaderboardSpec
	providers map[string]LeaderboardProvider
}

// NewReader builds a LeaderboardReader over the given data backend, resolving
// each registered game's spec and provider (a game's own provider if it
// implements LeaderboardCustom, else the default results provider).
func NewReader(data LeaderboardData, reg *Registry) LeaderboardReader {
	r := &reader{data: data, specs: map[string]LeaderboardSpec{}, providers: map[string]LeaderboardProvider{}}
	for _, g := range reg.All() {
		slug := g.Meta().Slug
		spec := ResolveLeaderboardSpec(g.Meta().Leaderboard)
		r.specs[slug] = spec
		if c, ok := g.(LeaderboardCustom); ok {
			r.providers[slug] = c.LeaderboardProvider(data)
		} else {
			r.providers[slug] = &defaultProvider{data: data, slug: slug, spec: spec}
		}
	}
	return r
}

func (r *reader) Spec(slug string) LeaderboardSpec {
	if s, ok := r.specs[slug]; ok {
		return s
	}
	return DefaultLeaderboardSpec
}

func (r *reader) provider(slug string) LeaderboardProvider {
	if p, ok := r.providers[slug]; ok {
		return p
	}
	return &defaultProvider{data: r.data, slug: slug, spec: r.Spec(slug)}
}

func (r *reader) Standings(ctx context.Context, slug string, w Window, limit, offset int) ([]Standing, error) {
	scores, err := r.provider(slug).Scores(ctx, w)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(scores))
	seen := map[string]struct{}{}
	for _, s := range scores {
		if _, ok := seen[s.AccountID]; ok {
			continue
		}
		seen[s.AccountID] = struct{}{}
		ids = append(ids, s.AccountID)
	}
	handles, err := r.data.ResolveHandles(ctx, ids)
	if err != nil {
		return nil, err
	}
	resolve := func(id string) (string, bool) {
		h, ok := handles[id]
		return h, ok
	}
	return RankStandings(scores, r.Spec(slug).Direction, resolve, limit, offset), nil
}

func (r *reader) PlayerStanding(ctx context.Context, slug, accountID string, w Window) (Standing, bool, error) {
	all, err := r.Standings(ctx, slug, w, 0, 0)
	if err != nil {
		return Standing{}, false, err
	}
	for _, s := range all {
		if s.AccountID == accountID {
			return s, true, nil
		}
	}
	return Standing{}, false, nil
}

// RankStandings turns provider Scores into ranked, handle-resolved Standings. It
// is the single place ordering, ranking, paging, and merge-exclusion live, so
// every reader (durable or in-memory) behaves identically regardless of where
// the Scores came from. resolve returns an account's live handle and whether it
// is live; a false drops the row (a merged/tombstoned or unknown account never
// appears). Ordering honors dir; ties break by earliest Achieved, then
// AccountID. Ranks are assigned before paging, so a later page keeps true ranks.
// limit <= 0 means no limit.
func RankStandings(scores []Score, dir Direction, resolve func(accountID string) (string, bool), limit, offset int) []Standing {
	type row struct {
		s      Score
		handle string
	}
	rows := make([]row, 0, len(scores))
	for _, s := range scores {
		h, live := resolve(s.AccountID)
		if !live {
			continue
		}
		rows = append(rows, row{s, h})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].s, rows[j].s
		if a.Value != b.Value {
			if dir == LowerBetter {
				return a.Value < b.Value
			}
			return a.Value > b.Value
		}
		if !a.Achieved.Equal(b.Achieved) {
			return a.Achieved.Before(b.Achieved)
		}
		return a.AccountID < b.AccountID
	})
	out := make([]Standing, len(rows))
	for i, r := range rows {
		out[i] = Standing{Rank: i + 1, AccountID: r.s.AccountID, Handle: r.handle, Value: r.s.Value, Achieved: r.s.Achieved}
	}
	return pageStandings(out, limit, offset)
}

// pageStandings slices one page out of an already-ranked board. Ranks were
// assigned before paging, so a later page keeps true ranks. limit <= 0 means
// no limit.
func pageStandings(out []Standing, limit, offset int) []Standing {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(out) {
		return []Standing{}
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

// NewCachedReader wraps inner with a short-TTL per-(slug, window) cache of the
// FULL ranked board (the limit-0 Standings slice). One cached slice serves
// every page flip, window revisit, and PlayerStanding rank lookup within the
// TTL — collapsing the lobby's repeated full-table aggregations (each of which
// otherwise holds one of the few pool connections) into one query per board
// per TTL. Boards are read-mostly and writes land asynchronously anyway, so a
// stale-by-seconds board is indistinguishable from a slightly-earlier read.
// Concurrent misses on one key are single-flighted. Errors are never cached.
// A ttl <= 0 returns inner unchanged (no caching).
func NewCachedReader(inner LeaderboardReader, ttl time.Duration) LeaderboardReader {
	if ttl <= 0 {
		return inner
	}
	return &cachedReader{inner: inner, ttl: ttl, now: time.Now, boards: map[boardKey]*boardEntry{}}
}

type boardKey struct {
	slug string
	w    Window
}

// boardEntry is one (slug, window) cache slot. ready is closed once the fetch
// that created the slot has filled standings/err — late arrivals block on it
// instead of issuing a duplicate query (single-flight).
type boardEntry struct {
	ready     chan struct{}
	standings []Standing // full ranked board; never mutated after ready closes
	err       error
	expires   time.Time
}

type cachedReader struct {
	inner LeaderboardReader
	ttl   time.Duration
	now   func() time.Time // injectable clock (tests)

	mu     sync.Mutex
	boards map[boardKey]*boardEntry
}

func (c *cachedReader) Spec(slug string) LeaderboardSpec { return c.inner.Spec(slug) }

func (c *cachedReader) Standings(ctx context.Context, slug string, w Window, limit, offset int) ([]Standing, error) {
	full, err := c.full(ctx, slug, w)
	if err != nil {
		return nil, err
	}
	return pageStandings(full, limit, offset), nil
}

func (c *cachedReader) PlayerStanding(ctx context.Context, slug, accountID string, w Window) (Standing, bool, error) {
	full, err := c.full(ctx, slug, w)
	if err != nil {
		return Standing{}, false, err
	}
	for _, s := range full {
		if s.AccountID == accountID {
			return s, true, nil
		}
	}
	return Standing{}, false, nil
}

// full returns the (possibly cached) full ranked board for one (slug, window).
func (c *cachedReader) full(ctx context.Context, slug string, w Window) ([]Standing, error) {
	key := boardKey{slug, w}
	c.mu.Lock()
	if e := c.boards[key]; e != nil {
		select {
		case <-e.ready: // filled: serve if still fresh, else fall through to refetch
			if c.now().Before(e.expires) {
				c.mu.Unlock()
				return e.standings, nil
			}
		default: // a fetch is in flight: wait on it instead of duplicating it
			c.mu.Unlock()
			select {
			case <-e.ready:
				return e.standings, e.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	// Miss (or expired): this caller fetches; the map slot single-flights any
	// concurrent caller onto e.ready. The lock is held from lookup through
	// insert, so exactly one fetcher replaces an expired entry.
	e := &boardEntry{ready: make(chan struct{})}
	c.boards[key] = e
	c.mu.Unlock()

	standings, err := c.inner.Standings(ctx, slug, w, 0, 0)
	c.mu.Lock()
	e.standings, e.err = standings, err
	e.expires = c.now().Add(c.ttl)
	if err != nil {
		// Never cache an error: drop the slot so the next caller retries
		// (current waiters still observe this err via ready).
		if c.boards[key] == e {
			delete(c.boards, key)
		}
	}
	close(e.ready)
	c.mu.Unlock()
	return standings, err
}
