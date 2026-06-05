// Package kittest is an in-memory test double for the kit authoring surface:
// a Room (plus Services/KV/config) you can drive from plain Go tests, with the
// sends, posts, and settle recorded for assertions.
//
//	r := kittest.NewRoom(kittest.Player("p1"), kittest.Player("p2"))
//	h := mygame.Game{}.NewRoom(r.Config(), r.Services())
//	h.OnStart(r)
//	h.OnJoin(r, r.Players[0])
//	h.OnInput(r, r.Players[0], kit.Input{Kind: kit.InputRune, Rune: ' '})
//	r.Advance(2 * time.Second) // move the room clock
//	h.OnWake(r)
//	frame := r.LastFrame(r.Players[0]) // assert on cells
//
// The clock is virtual (starts at a fixed epoch, moves only via Advance) and
// the RNG is seeded, so tests are deterministic by default — mirroring the
// wasm room, not the native dev runner's wall clock.
package kittest

import (
	"context"
	"math/rand"
	"time"

	kit "github.com/shellcade/kit/v2"
)

// Player builds a member Player with conventional fields from an id.
func Player(id string) kit.Player {
	return kit.Player{AccountID: id, Handle: id, Kind: kit.KindMember, Conn: "conn-" + id}
}

// Room is the in-memory kit.Room double.
type Room struct {
	Players []kit.Player
	Cfg     kit.RoomConfig
	Clock   time.Time
	RNG     *rand.Rand

	Frames   map[string][]*kit.Frame // accountID -> sent frames (copies)
	Ended    *kit.Result
	Posted   []kit.Result
	InputCtx kit.InputContext
	Logs     []string

	KV         map[string]map[string][]byte // accountID -> key -> value
	KVRules    map[string]map[string]kit.MergeRule
	ConfigVals map[string][]byte
}

// NewRoom returns a Room with the given members, a seeded RNG (seed 1), and a
// fixed-epoch virtual clock.
func NewRoom(players ...kit.Player) *Room {
	return &Room{
		Players:    players,
		Cfg:        kit.RoomConfig{Mode: kit.ModePrivate, Capacity: len(players), MinPlayers: 1, Seed: 1, SeedSet: true},
		Clock:      time.Unix(1_000_000, 0),
		RNG:        rand.New(rand.NewSource(1)),
		Frames:     map[string][]*kit.Frame{},
		KV:         map[string]map[string][]byte{},
		KVRules:    map[string]map[string]kit.MergeRule{},
		ConfigVals: map[string][]byte{},
	}
}

// Advance moves the virtual clock (use before OnWake to simulate time).
func (r *Room) Advance(d time.Duration) { r.Clock = r.Clock.Add(d) }

// LastFrame returns the most recent frame sent to p, or nil.
func (r *Room) LastFrame(p kit.Player) *kit.Frame {
	fs := r.Frames[p.AccountID]
	if len(fs) == 0 {
		return nil
	}
	return fs[len(fs)-1]
}

// ---- kit.Room ----------------------------------------------------------------

func (r *Room) Members() []kit.Player  { return r.Players }
func (r *Room) Count() int             { return len(r.Players) }
func (r *Room) Config() kit.RoomConfig { return r.Cfg }
func (r *Room) Rand() *rand.Rand       { return r.RNG }
func (r *Room) Now() time.Time         { return r.Clock }
func (r *Room) Settled() bool          { return r.Ended != nil }

func (r *Room) Has(p kit.Player) bool {
	for _, m := range r.Players {
		if m == p {
			return true
		}
	}
	return false
}

func (r *Room) Send(p kit.Player, f *kit.Frame) {
	if f == nil {
		return
	}
	cp := *f // record a copy: callers reuse frames via Clear()
	r.Frames[p.AccountID] = append(r.Frames[p.AccountID], &cp)
}

func (r *Room) Identical(f *kit.Frame) {
	for _, p := range r.Players {
		r.Send(p, f)
	}
}

func (r *Room) SetInputContext(c kit.InputContext) { r.InputCtx = c }
func (r *Room) End(res kit.Result)                 { r.Ended = &res }
func (r *Room) Post(res kit.Result)                { r.Posted = append(r.Posted, res) }
func (r *Room) Log(msg string)                     { r.Logs = append(r.Logs, msg) }

func (r *Room) Services() kit.Services {
	return kit.Services{Accounts: accounts{r}, Config: config{r}}
}

// ---- services doubles -----------------------------------------------------------

type accounts struct{ r *Room }

func (a accounts) For(p kit.Player) kit.Account { return account{a.r, p} }

type account struct {
	r *Room
	p kit.Player
}

func (a account) ID() string         { return a.p.AccountID }
func (a account) Handle() string     { return a.p.Handle }
func (a account) Kind() kit.Kind     { return a.p.Kind }
func (a account) Store() kit.KVStore { return kv{a.r, a.p.AccountID} }

type kv struct {
	r  *Room
	id string
}

func (k kv) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := k.r.KV[k.id][key]
	return v, ok, nil
}

func (k kv) Set(_ context.Context, key string, value []byte, rule kit.MergeRule) error {
	if k.r.KV[k.id] == nil {
		k.r.KV[k.id] = map[string][]byte{}
		k.r.KVRules[k.id] = map[string]kit.MergeRule{}
	}
	k.r.KV[k.id][key] = append([]byte(nil), value...)
	k.r.KVRules[k.id][key] = rule
	return nil
}

func (k kv) Delete(_ context.Context, key string) error {
	delete(k.r.KV[k.id], key)
	return nil
}

type config struct{ r *Room }

func (c config) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := c.r.ConfigVals[key]
	return v, ok, nil
}

// String renders a frame row as plain text — convenient in test failures.
func String(f *kit.Frame, row int) string {
	if f == nil || row < 0 || row >= kit.Rows {
		return ""
	}
	out := make([]rune, kit.Cols)
	for c := 0; c < kit.Cols; c++ {
		ru := f.Cells[row][c].Rune
		if ru == 0 {
			ru = ' '
		}
		out[c] = ru
	}
	return string(out)
}

var _ kit.Room = (*Room)(nil)
