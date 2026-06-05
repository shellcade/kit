// Package pokies is the game logic of the shellcade devkit reference game,
// factored out of the example's main package so it can be imported (e.g. for an
// in-process comparison against the wasm build). The thin examples/pokies main
// + exports wire kit.Main/kit.Run to Game{}; the behavior lives entirely here.
package pokies

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	kit "github.com/shellcade/kit"
)

// Game is the pokies registry entry: static metadata plus the per-room factory.
type Game struct{}

// Meta returns the static game metadata (mirrors the native pokies meta).
func (Game) Meta() kit.GameMeta {
	return kit.GameMeta{
		Slug:             "pokies",
		Name:             "Pokies",
		ShortDescription: "Pull the lever on your own slot machine and chase your high score.",
		MinPlayers:       1,
		MaxPlayers:       5,
		Tags:             []string{"slots", "casual"},

		QuickModeLabel:    "Quick spin",
		SoloModeLabel:     "Solo spin",
		PrivateInviteLine: "Friends join your floor when they enter the code.",

		Leaderboard: &kit.LeaderboardSpec{
			MetricLabel: "Credits",
			Direction:   kit.HigherBetter,
			Aggregation: kit.BestResult,
			Format:      kit.Integer,
		},
	}
}

// NewRoom returns the per-room behavior.
func (Game) NewRoom(cfg kit.RoomConfig, svc kit.Services) kit.Handler {
	return newRoom(cfg, svc)
}

const (
	startBalance = 1000
	rebuyAmount  = 1000
	tickerMult   = 12

	cycleRate    = 80 * time.Millisecond  // reel-cycling animation step
	reelStopBase = 150 * time.Millisecond // when the first reel settles
	reelStopStep = 250 * time.Millisecond // stagger between successive reels
	flashDur     = 1500 * time.Millisecond
	tickerDur    = 5 * time.Second

	configRefresh = 30 * time.Second
)

var betTiers = []int{10, 50, 100, 500}

// spinState is the live animation of one pull. The outcome is rolled up front;
// the wake idiom replaces the native timers: reel i lands when now passes
// startedAt + reelStopBase + i*reelStopStep, and the scroll cycle is DERIVED
// from elapsed time (hibernation-stable, heartbeat-rate independent).
type spinState struct {
	startedAt time.Time
	stopIdx   [3]int
	final     [3]symbol
	landed    int
	variant   *variant // pinned: a config refresh never re-evaluates an in-flight spin
}

func (s *spinState) cycle(now time.Time) int {
	return int(now.Sub(s.startedAt) / cycleRate)
}

// machine is one player's slot machine.
type machine struct {
	balance    int
	highScore  int
	bet        int
	reels      [3]symbol
	lastIdx    [3]int
	lastStrip  []symbol
	spun       bool
	spin       *spinState
	flash      string
	flashUntil time.Time
	postedPeak int // last peak posted to the leaderboard (post only on increase)
}

type ticker struct {
	text  string
	until time.Time
}

type room struct {
	kit.Base
	cfg kit.RoomConfig
	svc kit.Services

	machines map[string]*machine // keyed by account id (hibernation-safe)
	order    []string            // join order of account ids
	names    map[string]kit.Player
	ticker   ticker
	variant  *variant
	nextCfg  time.Time
	lastNow  time.Time
}

func newRoom(cfg kit.RoomConfig, svc kit.Services) *room {
	return &room{cfg: cfg, svc: svc, machines: map[string]*machine{}, names: map[string]kit.Player{}, variant: defaultVariant()}
}

func (rm *room) OnStart(r kit.Room) {
	r.SetInputContext(kit.CtxNav)
	rm.loadVariant(r)
	rm.nextCfg = r.Now().Add(configRefresh)
}

// loadVariant reads the odds variant from per-game config. A missing key or a
// bad document keeps the last good variant, mirroring the native game.
func (rm *room) loadVariant(r kit.Room) {
	blob, ok, err := r.Services().Config.Get(context.Background(), "odds-variant")
	if err != nil || !ok {
		rm.variant = defaultVariant()
		return
	}
	if v, err := parseVariant(blob); err == nil {
		rm.variant = v
	} else {
		r.Log("pokies: stored odds variant is invalid; using default")
		rm.variant = defaultVariant()
	}
}

// wallet: the casino pattern over kv — balance (sum) and peak (max), the same
// keys and merge rules as the native casino package.

func kvInt(store kit.KVStore, key string) (int, bool) {
	v, ok, err := store.Get(context.Background(), key)
	if err != nil || !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(v)))
	if err != nil {
		return 0, false
	}
	return n, true
}

func (rm *room) seedWallet(r kit.Room, p kit.Player) (int, int) {
	acct := r.Services().Accounts.For(p)
	if acct == nil {
		return startBalance, startBalance
	}
	store := acct.Store()
	bal, ok := kvInt(store, "balance")
	if !ok || bal <= 0 {
		bal = startBalance
	}
	peak, ok := kvInt(store, "peak")
	if !ok || peak < bal {
		peak = bal
	}
	return bal, peak
}

func (rm *room) persistWallet(r kit.Room, p kit.Player, bal, peak int) {
	acct := r.Services().Accounts.For(p)
	if acct == nil {
		return
	}
	store := acct.Store()
	_ = store.Set(context.Background(), "balance", []byte(strconv.Itoa(bal)), kit.MergeSum)
	_ = store.Set(context.Background(), "peak", []byte(strconv.Itoa(peak)), kit.MergeMax)
}

func (rm *room) OnJoin(r kit.Room, p kit.Player) {
	rm.names[p.AccountID] = p
	if _, ok := rm.machines[p.AccountID]; ok {
		rm.render(r)
		return
	}
	bal, peak := rm.seedWallet(r, p)
	rm.machines[p.AccountID] = &machine{balance: bal, highScore: peak, bet: betTiers[0]}
	rm.order = append(rm.order, p.AccountID)
	rm.render(r)
}

func (rm *room) OnLeave(r kit.Room, p kit.Player) {
	m := rm.machines[p.AccountID]
	if m == nil {
		return
	}
	rm.persistWallet(r, p, m.balance, m.highScore)
	delete(rm.machines, p.AccountID)
	delete(rm.names, p.AccountID)
	for i, id := range rm.order {
		if id == p.AccountID {
			rm.order = append(rm.order[:i], rm.order[i+1:]...)
			break
		}
	}
	rm.render(r)
}

func (rm *room) OnInput(r kit.Room, p kit.Player, in kit.Input) {
	m := rm.machines[p.AccountID]
	if m == nil {
		return
	}
	switch kit.Resolve(in, kit.CtxNav) {
	case kit.ActUp:
		rm.adjustBet(m, +1)
	case kit.ActDown:
		rm.adjustBet(m, -1)
	case kit.ActConfirm:
		rm.startSpin(r, p)
	}
	rm.render(r)
}

// OnWake advances every time-driven element against CallContext time, then
// renders: reel landings, flash expiry, and the periodic config refresh.
func (rm *room) OnWake(r kit.Room) {
	now := r.Now()
	if !rm.nextCfg.IsZero() && now.After(rm.nextCfg) {
		rm.loadVariant(r)
		rm.nextCfg = now.Add(configRefresh)
	}
	for id, m := range rm.machines {
		if m.flash != "" && now.After(m.flashUntil) {
			m.flash = ""
		}
		if m.spin == nil {
			continue
		}
		for i := m.spin.landed; i < 3; i++ {
			due := m.spin.startedAt.Add(reelStopBase + time.Duration(i)*reelStopStep)
			if !now.After(due) {
				break
			}
			rm.landReel(r, id, i)
			if m.spin == nil {
				break
			}
		}
	}
	rm.render(r)
}

func (rm *room) OnClose(r kit.Room) {
	for id, m := range rm.machines {
		if p, ok := rm.names[id]; ok {
			rm.persistWallet(r, p, m.balance, m.highScore)
		}
	}
}

// --- betting -----------------------------------------------------------------

func tierIndex(bet int) int {
	for i, t := range betTiers {
		if t == bet {
			return i
		}
	}
	return 0
}

func (rm *room) adjustBet(m *machine, dir int) {
	i := tierIndex(m.bet) + dir
	if i < 0 {
		i = 0
	}
	if i >= len(betTiers) {
		i = len(betTiers) - 1
	}
	m.bet = betTiers[i]
	rm.clampBet(m)
}

func (rm *room) clampBet(m *machine) {
	for m.bet > m.balance && tierIndex(m.bet) > 0 {
		m.bet = betTiers[tierIndex(m.bet)-1]
	}
}

// --- spinning ------------------------------------------------------------------

func (rm *room) startSpin(r kit.Room, p kit.Player) {
	m := rm.machines[p.AccountID]
	if m == nil || m.spin != nil {
		return
	}
	rm.clampBet(m)
	if m.bet > m.balance {
		return
	}
	m.balance -= m.bet
	m.flash = ""

	v := rm.variant
	if v == nil {
		v = defaultVariant()
	}
	s := &spinState{startedAt: r.Now(), variant: v}
	for i := range s.final {
		s.stopIdx[i] = r.Rand().Intn(len(v.strip))
		s.final[i] = v.strip[s.stopIdx[i]]
	}
	m.spin = s
}

func (rm *room) landReel(r kit.Room, id string, i int) {
	m := rm.machines[id]
	if m == nil || m.spin == nil {
		return
	}
	m.spin.landed = i + 1
	m.reels[i] = m.spin.final[i]
	m.lastIdx[i] = m.spin.stopIdx[i]
	if v := m.spin.variant; v != nil {
		m.lastStrip = v.strip
	}
	if m.spin.landed >= 3 {
		rm.settleSpin(r, id)
	}
}

func (rm *room) settleSpin(r kit.Room, id string) {
	m := rm.machines[id]
	if m == nil || m.spin == nil {
		return
	}
	m.reels = m.spin.final
	m.lastIdx = m.spin.stopIdx
	v := m.spin.variant
	if v == nil {
		v = defaultVariant()
	}
	m.lastStrip = v.strip
	m.spin = nil
	m.spun = true

	mult := v.payout(m.reels)
	win := m.bet * mult
	m.balance += win
	if m.balance > m.highScore {
		m.highScore = m.balance
	}
	if mult >= tickerMult {
		if p, ok := rm.names[id]; ok {
			rm.ticker = ticker{
				text:  fmt.Sprintf("%s hit a big win  +%d", p.DisplayName(), win),
				until: r.Now().Add(tickerDur),
			}
		}
	}

	switch {
	case m.balance <= 0:
		m.balance = rebuyAmount
		m.flash = "RE-BUY"
	case win > 0:
		m.flash = fmt.Sprintf("WIN! +%d", win)
	default:
		m.flash = ""
	}
	m.flashUntil = r.Now().Add(flashDur)
	rm.clampBet(m)
	if p, ok := rm.names[id]; ok {
		rm.persistWallet(r, p, m.balance, m.highScore)
		// Leaderboard: Post feeds the board declared in GameMeta.Leaderboard
		// (Credits, higher-better, best-result). Post on a new personal peak —
		// the board keeps each account's best posted Metric. This is THE way a
		// score reaches the board; KV is durable state, not the leaderboard.
		if m.highScore > m.postedPeak {
			m.postedPeak = m.highScore
			r.Post(kit.Result{Rankings: []kit.PlayerResult{{
				Player: p, Metric: m.highScore, Status: kit.StatusFinished,
			}}})
		}
	}
}

func (rm *room) tickerActive(now time.Time) bool {
	return rm.ticker.text != "" && now.Before(rm.ticker.until)
}
