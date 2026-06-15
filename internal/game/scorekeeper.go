package game

import (
	"context"
	"sort"
	"strconv"
	"sync"
)

// Cadence controls when ScoreKeeper.Record auto-posts a player's metric.
type Cadence int

const (
	// OnImprove posts only when the new metric beats the last posted value —
	// the right choice for monotonic high-water boards (peak credits, best
	// survival time, kill count).
	OnImprove Cadence = iota
	// OnChange posts whenever the metric changes from the last posted value.
	OnChange
)

// ScoreKeeper tracks each player's current leaderboard metric and standardises
// posting it three ways: live (Record), on disconnect (FlushLeave), and — for
// continuous "never-ending" games — periodically (FlushAll).
//
// It holds NO goroutines or timers: periodic flushing is driven by the game's
// own OnWake heartbeat, so behaviour stays deterministic under hibernation and
// replay. A single keeper is held on the room and is safe for the room actor.
//
// The board itself is fed only by the Post calls this makes; PersistBest /
// PersistWallet write per-account KV for session *resume*, which is separate
// from the leaderboard.
type ScoreKeeper struct {
	mu      sync.Mutex
	cadence Cadence
	cur     map[string]int
	posted  map[string]int
	players map[string]Player
}

// NewScoreKeeper returns a ScoreKeeper with the given auto-post cadence.
func NewScoreKeeper(c Cadence) *ScoreKeeper {
	return &ScoreKeeper{
		cadence: c,
		cur:     map[string]int{},
		posted:  map[string]int{},
		players: map[string]Player{},
	}
}

// Record updates the player's current metric and posts it per the cadence.
// Live posts always carry StatusFinished.
func (sk *ScoreKeeper) Record(r Room, p Player, metric int) {
	sk.mu.Lock()
	sk.cur[p.AccountID] = metric
	sk.players[p.AccountID] = p
	last, seen := sk.posted[p.AccountID]
	should := !seen
	switch sk.cadence {
	case OnImprove:
		should = should || metric > last
	case OnChange:
		should = should || metric != last
	}
	if should {
		sk.posted[p.AccountID] = metric
	}
	sk.mu.Unlock()
	if should {
		r.Post(Result{Rankings: []PlayerResult{{Player: p, Metric: metric, Status: StatusFinished}}})
	}
}

// FlushLeave posts the player's current tracked metric with the given status
// (normally StatusDNF) and stops tracking them. Call it from OnLeave so a
// mid-game disconnect still records the player's progress. Calling it for an
// untracked player is a no-op.
//
// IMPORTANT: the platform's leaderboard reader ranks DNF rows the same as
// finished ones. For a lower-is-better board, pass a fair full-run metric
// (e.g. par-extrapolated), never a raw partial, or a half-played run will top
// the board.
func (sk *ScoreKeeper) FlushLeave(r Room, p Player, status Status) {
	sk.mu.Lock()
	metric, ok := sk.cur[p.AccountID]
	delete(sk.cur, p.AccountID)
	delete(sk.posted, p.AccountID)
	delete(sk.players, p.AccountID)
	sk.mu.Unlock()
	if !ok {
		return
	}
	r.Post(Result{Rankings: []PlayerResult{{Player: p, Metric: metric, Status: status}}})
}

// FlushAll posts every tracked player's current metric with the given status,
// in deterministic AccountID order. Continuous games call this from OnWake on
// a throttled interval so an abandoned, still-ticking world keeps recording.
func (sk *ScoreKeeper) FlushAll(r Room, status Status) {
	sk.mu.Lock()
	ids := make([]string, 0, len(sk.cur))
	for id := range sk.cur {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type row struct {
		p Player
		m int
	}
	rows := make([]row, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, row{sk.players[id], sk.cur[id]})
		sk.posted[id] = sk.cur[id]
	}
	sk.mu.Unlock()
	for _, rw := range rows {
		r.Post(Result{Rankings: []PlayerResult{{Player: rw.p, Metric: rw.m, Status: status}}})
	}
}

// PersistBest writes a monotonic high-water value to the player's per-account
// KV (MergeMax) for session resume. The leaderboard board is fed by
// Record/FlushLeave/FlushAll; this only preserves state across reconnects.
func (sk *ScoreKeeper) PersistBest(r Room, p Player, key string, value int) {
	acct := r.Services().Accounts.For(p)
	if acct == nil {
		return
	}
	_ = acct.Store().Set(context.Background(), key, []byte(strconv.Itoa(value)), MergeMax)
}

// PersistWallet writes a carryable balance (MergeSum) and a high-water peak
// (MergeMax) for casino-style games, replacing the duplicated persistWallet
// helpers. MergeMax/MergeSum make a KV-outage-era write unable to clobber the
// durable value at merge time.
func (sk *ScoreKeeper) PersistWallet(r Room, p Player, balanceKey string, balance int, peakKey string, peak int) {
	acct := r.Services().Accounts.For(p)
	if acct == nil {
		return
	}
	st := acct.Store()
	_ = st.Set(context.Background(), balanceKey, []byte(strconv.Itoa(balance)), MergeSum)
	_ = st.Set(context.Background(), peakKey, []byte(strconv.Itoa(peak)), MergeMax)
}
