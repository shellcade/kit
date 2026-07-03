// Package memsvc is a public, in-memory implementation of the host-side
// [sdk.ServicesFactory] (and the matching [sdk.AccountStore], [sdk.KVStore],
// [sdk.ConfigStore], and leaderboard surfaces). It lets the CLI dev runner and
// the conformance suite run games WITHOUT the platform's private Postgres /
// identity services: everything lives in process memory.
//
// It implements the SAME sdk interfaces the production durable (Postgres)
// factory does — a fire-and-forget [sdk.LeaderboardClient].Post, a per-user KV
// behind an [sdk.AccountStore], a slug-bound read-only [sdk.ConfigStore], and an
// [sdk.LeaderboardData] backend a generic [sdk.LeaderboardReader] composes — and
// matches their observable contract:
//
//   - Leaderboard recording records every account-bound result tagged with mode
//   - status, dropping only guests (an empty AccountID). It lives here, not in
//     game code; games always Post and never branch on eligibility.
//   - Per-user KV is namespaced to (slug, account, key). A key written with the
//     [sdk.MergeMax] rule is kept MONOTONIC on write — the stored value only ever
//     rises — mirroring the durable store, so out-of-order writers can never
//     regress a max key. All other rules overwrite last-writer-wins; the
//     sum/max/keep-loser accumulation ACROSS accounts happens only at an account
//     merge ([Factory.Merge]).
//   - Per-game config is a slug-bound, read-only surface seeded by the harness
//     ([Factory.SetConfig]); games can read only their own slug's keys.
//
// The package imports only github.com/shellcade/kit/v2/host/sdk — no shellcade
// private code — which is the property that lets the kit ship a runnable host.
package memsvc

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// Factory is an in-memory [sdk.ServicesFactory] plus an [sdk.LeaderboardData]
// backend (reachable via [Factory.Reader]). It is safe for concurrent use.
type Factory struct {
	log *slog.Logger
	reg *sdk.Registry

	mu      sync.Mutex
	results map[string][]rec  // slug -> recorded results
	handles map[string]string // accountID -> latest seen handle (all "live" in memory)

	kv      *memKV
	cfg     *memConfig
	credits map[string]*memCreditsAccount // accountID -> in-memory wallet
}

// rec is one recorded leaderboard result row.
type rec struct {
	accountID string
	metric    int
	achieved  time.Time
	mode      sdk.Mode
	status    sdk.Status
}

// New returns an in-memory [sdk.ServicesFactory] over the package-level default
// game registry ([sdk.Default]). This is the drop-in for the CLI dev runner and
// conformance: the same shape as the durable factory's constructor minus the
// platform-only Postgres store argument. Use [NewFactory] when you need the
// concrete *Factory (for [Factory.Reader], [Factory.SetConfig], or
// [Factory.Merge]) or a curated registry.
func New() sdk.ServicesFactory { return NewFactory(nil, nil) }

// NewWithRegistry returns an in-memory [sdk.ServicesFactory] over an explicit
// registry (a curated roster for the CLI or a conformance fixture).
func NewWithRegistry(log *slog.Logger, reg *sdk.Registry) sdk.ServicesFactory {
	return NewFactory(log, reg)
}

// NewFactory returns the concrete in-memory *Factory over an explicit logger
// and registry. A nil log defaults to [slog.Default]; a nil registry defaults to
// [sdk.Default]. Prefer [New] when you only need the [sdk.ServicesFactory]
// interface; use this when a test or the CLI also needs the read side
// ([Factory.Reader]), config seeding ([Factory.SetConfig]), or account merge
// ([Factory.Merge]).
func NewFactory(log *slog.Logger, reg *sdk.Registry) *Factory {
	if log == nil {
		log = slog.Default()
	}
	if reg == nil {
		reg = sdk.Default()
	}
	return &Factory{
		log:     log,
		reg:     reg,
		results: map[string][]rec{},
		handles: map[string]string{},
		kv:      &memKV{m: map[kvKey]kvVal{}},
		cfg:     &memConfig{m: map[cfgKey][]byte{}},
		credits: map[string]*memCreditsAccount{},
	}
}

// For builds a [sdk.Services] bundle tagged with the room id and game slug. The
// returned AccountStore, ConfigStore, and LeaderboardClient are all bound to the
// slug, so a game can reach only its own per-game state.
func (f *Factory) For(roomID, slug string) sdk.Services {
	svc := sdk.Services{
		Leaderboard: &memLeaderboard{f: f},
		Accounts:    &memAccounts{f: f, slug: slug},
		Config:      &memConfigStore{cfg: f.cfg, slug: slug},
		Chat:        noopChat{},
		Spectate:    noopSpectate{},
		Log:         f.log.With("room", roomID, "slug", slug),
	}
	// Casino-kind games get the in-memory credits backend (seed 1000, escrow
	// per room+account, gross settle clamped by the game's declared
	// multiplier where the registry knows it). Game-kind guests never reach
	// it — the gameabi host rejects their calls before the service.
	svc.Credits = &memCredits{f: f, roomID: roomID, slug: slug}
	return svc
}

// Reader exposes the leaderboard read side (the lobby/UI surface, never handed
// to games), composing this factory's in-memory data backend with the registry's
// per-game specs and providers. Reads reflect every result recorded via the
// LeaderboardClient.Post returned by [Factory.For].
func (f *Factory) Reader() sdk.LeaderboardReader {
	return sdk.NewReader(&memData{f: f}, f.reg)
}

// record stores the latest handle seen for an account (every in-memory account
// is treated as live, so handle resolution never excludes it). Callers hold f.mu.
func (f *Factory) record(accountID, handle string) {
	if accountID == "" {
		return
	}
	f.handles[accountID] = handle
}

// memLeaderboard is the fire-and-forget write side: record every account-bound
// result tagged with mode + status, dropping only guests.
type memLeaderboard struct{ f *Factory }

func (l *memLeaderboard) Post(slug string, r sdk.Result) {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	now := time.Now()
	for _, pr := range r.Rankings {
		if pr.Player.AccountID == "" {
			continue // guests carry no account: never recorded
		}
		l.f.results[slug] = append(l.f.results[slug], rec{
			accountID: pr.Player.AccountID,
			metric:    pr.Metric,
			achieved:  now,
			mode:      r.Mode,
			status:    pr.Status,
		})
		l.f.record(pr.Player.AccountID, pr.Player.Handle)
	}
}

// ---- credits (casino-kind games) ----

// memCreditsAccount is one account's in-memory wallet: a balance seeded at
// 1000 plus the per-room open stakes.
type memCreditsAccount struct {
	balance int64
	stakes  map[string]int64 // roomID -> open stake
}

// memCredits implements sdk.CreditsService for `check`/`play`/conformance:
// the real economy rules that matter to a guest (atomic escrow, gross settle,
// declared-multiplier clamp) with none of the platform's persistence.
type memCredits struct {
	f      *Factory
	roomID string
	slug   string
}

func (c *memCredits) account(p sdk.Player) *memCreditsAccount {
	acc, ok := c.f.credits[p.AccountID]
	if !ok {
		acc = &memCreditsAccount{balance: 1000, stakes: map[string]int64{}}
		c.f.credits[p.AccountID] = acc
	}
	return acc
}

func (c *memCredits) Balance(_ context.Context, p sdk.Player) (int64, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.account(p).balance, nil
}

func (c *memCredits) Wager(_ context.Context, p sdk.Player, amount int64) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if amount <= 0 {
		return sdk.ErrCreditsDenied
	}
	acc := c.account(p)
	if amount > acc.balance {
		return sdk.ErrInsufficientCredits
	}
	acc.balance -= amount
	acc.stakes[c.roomID] += amount
	return nil
}

func (c *memCredits) Settle(_ context.Context, p sdk.Player, payout int64) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	acc := c.account(p)
	stake := acc.stakes[c.roomID]
	if stake == 0 {
		return sdk.ErrCreditsDenied
	}
	if payout < 0 {
		payout = 0
	}
	// Clamp to the game's declared multiplier when the registry knows it —
	// the same rule a production host applies, so `check` exercises it.
	if g, ok := c.f.reg.Get(c.slug); ok {
		if m := g.Meta().MaxPayoutMultiplier; m > 0 && payout > stake*int64(m) {
			payout = stake * int64(m)
		}
	}
	delete(acc.stakes, c.roomID)
	acc.balance += payout
	return nil
}

// ---- per-user KV ----

type kvKey struct{ slug, account, key string }
type kvVal struct {
	val  []byte
	rule sdk.MergeRule
}

type memKV struct {
	mu sync.Mutex
	m  map[kvKey]kvVal
}

type memAccounts struct {
	f    *Factory
	slug string
}

func (a *memAccounts) For(p sdk.Player) sdk.Account {
	a.f.mu.Lock()
	a.f.record(p.AccountID, p.Handle)
	a.f.mu.Unlock()
	return &memAccount{kv: a.f.kv, slug: a.slug, p: p}
}

type memAccount struct {
	kv   *memKV
	slug string
	p    sdk.Player
}

func (a *memAccount) ID() string     { return a.p.AccountID }
func (a *memAccount) Handle() string { return a.p.Handle }
func (a *memAccount) Kind() sdk.Kind { return a.p.Kind }
func (a *memAccount) Store() sdk.KVStore {
	return &memKVStore{kv: a.kv, slug: a.slug, account: a.p.AccountID}
}

type memKVStore struct {
	kv      *memKV
	slug    string
	account string
}

func (s *memKVStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.kv.mu.Lock()
	defer s.kv.mu.Unlock()
	v, ok := s.kv.m[kvKey{s.slug, s.account, key}]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v.val...), true, nil
}

func (s *memKVStore) Set(ctx context.Context, key string, val []byte, rule sdk.MergeRule) error {
	s.kv.mu.Lock()
	defer s.kv.mu.Unlock()
	k := kvKey{s.slug, s.account, key}
	// Mirror the durable store: a `max` key is kept monotonic on write — the
	// stored value only ever rises, so an out-of-order writer can't regress it.
	if rule == sdk.MergeMax {
		if cur, ok := s.kv.m[k]; ok {
			curN, e1 := strconv.Atoi(strings.TrimSpace(string(cur.val)))
			newN, e2 := strconv.Atoi(strings.TrimSpace(string(val)))
			if e1 == nil && e2 == nil && curN >= newN {
				return nil
			}
		}
	}
	s.kv.m[k] = kvVal{val: append([]byte(nil), val...), rule: rule}
	return nil
}

func (s *memKVStore) Delete(ctx context.Context, key string) error {
	s.kv.mu.Lock()
	defer s.kv.mu.Unlock()
	delete(s.kv.m, kvKey{s.slug, s.account, key})
	return nil
}

// Merge folds the loser account's per-user KV into the winner, applying each
// key's recorded [sdk.MergeRule] on a collision and moving non-colliding keys
// across unchanged, then dropping the loser's rows. It is the in-memory twin of
// the durable store's account-merge reconciliation; the CLI/conformance can call
// it to exercise the merge-rule semantics without an identity service. The
// KVStore games hold has no merge method (the rule is only RECORDED on Set) —
// this models the platform-owned merge that consumes those recorded rules.
//
// Per the ABI contract: the winner's recorded rule governs the collision
// (falling back to the loser's when the winner has none); keep-winner leaves the
// winner's value untouched; keep-loser takes the loser's; sum/max combine the two
// base-10 integers, and a non-integer value under sum/max DEGRADES to keep-winner
// (so poisoned game data can never break an account merge). An empty/unknown rule
// is keep-winner.
func (f *Factory) Merge(winnerID, loserID string) {
	if winnerID == "" || loserID == "" || winnerID == loserID {
		return
	}
	f.kv.mu.Lock()
	defer f.kv.mu.Unlock()
	for k, lr := range f.kv.m {
		if k.account != loserID {
			continue
		}
		wk := kvKey{k.slug, winnerID, k.key}
		win, collision := f.kv.m[wk]
		if !collision {
			// No collision: move the loser's row (value + rule) to the winner.
			f.kv.m[wk] = lr
			delete(f.kv.m, k)
			continue
		}
		// Collision: the winner's recorded rule wins, falling back to the loser's.
		rule := win.rule
		if rule == "" {
			rule = lr.rule
		}
		if v, ok := mergedValue(rule, win.val, lr.val); ok {
			win.val = v
		}
		// keep-winner (and a degraded sum/max) leaves the winner's value as-is.
		win.rule = rule
		f.kv.m[wk] = win
		delete(f.kv.m, k)
	}
}

// mergedValue applies one merge rule to a colliding (winner, loser) pair,
// returning the new winner value and whether it changed. keep-winner — and a
// sum/max collision whose values are not both base-10 integers — returns
// ok=false (the winner's value is kept).
func mergedValue(rule sdk.MergeRule, winVal, loserVal []byte) ([]byte, bool) {
	switch rule {
	case sdk.MergeKeepLoser:
		return append([]byte(nil), loserVal...), true
	case sdk.MergeSum, sdk.MergeMax:
		wn, e1 := strconv.Atoi(strings.TrimSpace(string(winVal)))
		ln, e2 := strconv.Atoi(strings.TrimSpace(string(loserVal)))
		if e1 != nil || e2 != nil {
			return nil, false // degrade to keep-winner on non-integer data
		}
		if rule == sdk.MergeSum {
			return []byte(strconv.Itoa(wn + ln)), true
		}
		if ln > wn {
			return []byte(strconv.Itoa(ln)), true
		}
		return []byte(strconv.Itoa(wn)), true
	default: // keep-winner / empty / unknown
		return nil, false
	}
}

// ---- per-game config ----

type cfgKey struct{ slug, key string }

type memConfig struct {
	mu sync.Mutex
	m  map[cfgKey][]byte
}

// SetConfig seeds a per-game config value into the in-memory backend, so the CLI
// or a conformance test can exercise a game's config-driven behavior without a
// database. It mirrors the durable ConfigSet: last-write-wins, global per
// (slug, key).
func (f *Factory) SetConfig(slug, key string, value []byte) {
	f.cfg.mu.Lock()
	defer f.cfg.mu.Unlock()
	f.cfg.m[cfgKey{slug, key}] = append([]byte(nil), value...)
}

// memConfigStore is the slug-bound, read-only config surface (the in-memory twin
// of the durable config store): the binding owns the slug, the game names only
// the key, so it can neither read nor write another game's config.
type memConfigStore struct {
	cfg  *memConfig
	slug string
}

func (s *memConfigStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.cfg.mu.Lock()
	defer s.cfg.mu.Unlock()
	v, ok := s.cfg.m[cfgKey{s.slug, key}]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

// ---- LeaderboardData backend ----

type memData struct{ f *Factory }

func (d *memData) ResultScores(ctx context.Context, slug string, spec sdk.LeaderboardSpec, w sdk.Window) ([]sdk.Score, error) {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	start, bounded := sdk.WindowStart(w, time.Now())
	type agg struct {
		value    int
		achieved time.Time
		set      bool
	}
	m := map[string]*agg{}
	for _, r := range d.f.results[slug] {
		if bounded && r.achieved.Before(start) {
			continue
		}
		a := m[r.accountID]
		if a == nil {
			a = &agg{}
			m[r.accountID] = a
		}
		switch spec.Aggregation {
		case sdk.CumulativeSum:
			a.value += r.metric
			if !a.set || r.achieved.Before(a.achieved) {
				a.achieved = r.achieved
			}
			a.set = true
		default: // BestResult
			better := !a.set
			if a.set {
				if spec.Direction == sdk.LowerBetter {
					better = r.metric < a.value
				} else {
					better = r.metric > a.value
				}
			}
			switch {
			case better:
				a.value = r.metric
				a.achieved = r.achieved
				a.set = true
			case r.metric == a.value && r.achieved.Before(a.achieved):
				a.achieved = r.achieved
			}
		}
	}
	out := make([]sdk.Score, 0, len(m))
	for id, a := range m {
		out = append(out, sdk.Score{AccountID: id, Value: a.value, Achieved: a.achieved})
	}
	return out, nil
}

func (d *memData) KVIntValues(ctx context.Context, slug, key string) ([]sdk.Score, error) {
	d.f.kv.mu.Lock()
	defer d.f.kv.mu.Unlock()
	var out []sdk.Score
	for k, v := range d.f.kv.m {
		if k.slug != slug || k.key != key {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(v.val)))
		if err != nil {
			continue
		}
		out = append(out, sdk.Score{AccountID: k.account, Value: n})
	}
	return out, nil
}

func (d *memData) ResolveHandles(ctx context.Context, ids []string) (map[string]string, error) {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if h, ok := d.f.handles[id]; ok {
			out[id] = h
		}
	}
	return out, nil
}

// ---- v1 no-op stubs ----

type noopChat struct{}

func (noopChat) Broadcast(roomID, from, msg string) {}

type noopSpectate struct{}

func (noopSpectate) Open(roomID string) error { return nil }

// Interface guards: memsvc implements the host-side sdk surfaces.
var (
	_ sdk.ServicesFactory   = (*Factory)(nil)
	_ sdk.LeaderboardClient = (*memLeaderboard)(nil)
	_ sdk.AccountStore      = (*memAccounts)(nil)
	_ sdk.Account           = (*memAccount)(nil)
	_ sdk.KVStore           = (*memKVStore)(nil)
	_ sdk.ConfigStore       = (*memConfigStore)(nil)
	_ sdk.LeaderboardData   = (*memData)(nil)
)
