package smoke

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"time"

	"github.com/shellcade/kit/v2/internal/game"
)

// Run executes a parsed script against the game natively: a virtual-clock,
// seeded room (no terminal, no wall clock) mirroring the dev runner's -seed
// semantics. All seats join before the first step; `advance` moves the clock
// one heartbeat at a time, waking the game after each increment; shots
// capture the latest frame sent to each captured seat.
//
// Identities are the smoke contract shared with the devkit CLI's wasm path:
// seat i is AccountID "seat-<i>", Handle "seat<i>", member kind. The room is
// ModeSolo for one seat and ModePrivate otherwise, with Capacity = seats.
func Run(g game.Game, s *Script) ([]Shot, error) {
	meta := g.Meta()
	if s.Seats > meta.MaxPlayers {
		return nil, fmt.Errorf("smoke: seats %d exceeds the game's maxPlayers %d", s.Seats, meta.MaxPlayers)
	}

	players := make([]game.Player, s.Seats)
	for i := range players {
		players[i] = game.Player{
			AccountID: fmt.Sprintf("seat-%d", i),
			Handle:    fmt.Sprintf("seat%d", i),
			Kind:      game.KindMember,
			Conn:      fmt.Sprintf("conn-%d", i),
		}
	}
	mode := game.ModePrivate
	if s.Seats == 1 {
		mode = game.ModeSolo
	}
	cfg := game.RoomConfig{
		Mode:       mode,
		Capacity:   s.Seats,
		MinPlayers: min(meta.MinPlayers, s.Seats),
		Seed:       s.Seed,
		SeedSet:    true,
	}
	r := &room{
		cfg:    cfg,
		rng:    rand.New(rand.NewSource(s.Seed)),
		clock:  game.SeedEpoch(s.Seed),
		frames: map[string]*game.Frame{},
		kv:     map[string][]byte{},
		config: s.Config,
	}

	h := g.NewRoom(cfg, r.Services())
	h.OnStart(r)
	for i, p := range players {
		r.members = players[:i+1]
		h.OnJoin(r, p)
	}
	r.members = players

	var shots []Shot
	cur := 0
	for _, st := range s.Steps {
		switch st.Kind {
		case StepRune:
			h.OnInput(r, players[cur], game.Input{Kind: game.InputRune, Rune: st.Rune})
		case StepKey:
			h.OnInput(r, players[cur], game.Input{Kind: game.InputKey, Key: st.Key})
		case StepSeat:
			cur = st.Seat
		case StepAdvance:
			for elapsed := time.Duration(0); elapsed < st.D; elapsed += s.Heartbeat {
				r.clock = r.clock.Add(s.Heartbeat)
				h.OnWake(r)
			}
		case StepWake:
			h.OnWake(r)
		case StepShot:
			seats := st.Seats
			if seats == nil {
				seats = make([]int, s.Seats)
				for i := range seats {
					seats[i] = i
				}
			} else {
				seats = slices.Clone(seats)
				slices.Sort(seats)
			}
			shot := Shot{Ordinal: len(shots) + 1, Name: st.Name, Seats: seats}
			for _, seat := range seats {
				f := r.frames[players[seat].AccountID]
				if f == nil {
					return nil, fmt.Errorf("smoke.yaml:%d: shot %q: seat %d has no frame yet — the game has not rendered for it", st.Line, st.Name, seat)
				}
				shot.Frames = append(shot.Frames, f)
			}
			shots = append(shots, shot)
		}
	}
	h.OnClose(r)
	return shots, nil
}

// room is the smoke-run Room: in-memory, virtual clock, latest-frame-per-seat
// capture. It mirrors kittest.Room but records only what shots need.
type room struct {
	cfg     game.RoomConfig
	members []game.Player
	rng     *rand.Rand
	clock   time.Time
	frames  map[string]*game.Frame // accountID -> latest frame (copy)
	kv      map[string][]byte      // account + key
	config  map[string]string
	ended   bool
}

func (r *room) Members() []game.Player  { return r.members }
func (r *room) Count() int              { return len(r.members) }
func (r *room) Config() game.RoomConfig { return r.cfg }
func (r *room) Rand() *rand.Rand        { return r.rng }
func (r *room) Now() time.Time          { return r.clock }
func (r *room) Settled() bool           { return r.ended }

func (r *room) Has(p game.Player) bool {
	for _, m := range r.members {
		if m == p {
			return true
		}
	}
	return false
}

func (r *room) Send(p game.Player, f *game.Frame) {
	if f == nil {
		return
	}
	cp := *f // games reuse frames via Clear(); keep the moment's copy
	r.frames[p.AccountID] = &cp
}

func (r *room) Identical(f *game.Frame) {
	for _, p := range r.members {
		r.Send(p, f)
	}
}

func (r *room) SetInputContext(game.InputContext) {}
func (r *room) End(game.Result)                   { r.ended = true }
func (r *room) Post(game.Result)                  {}
func (r *room) Log(string)                        {}

func (r *room) Services() game.Services {
	return game.Services{Accounts: accounts{r}, Config: configStore{r}}
}

type accounts struct{ r *room }

func (a accounts) For(p game.Player) game.Account { return account{a.r, p} }

type account struct {
	r *room
	p game.Player
}

func (a account) ID() string          { return a.p.AccountID }
func (a account) Handle() string      { return a.p.Handle }
func (a account) Kind() game.Kind     { return a.p.Kind }
func (a account) Store() game.KVStore { return kv{a.r, a.p.AccountID} }

type kv struct {
	r       *room
	account string
}

func (k kv) key(s string) string { return k.account + "\x00" + s }

func (k kv) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := k.r.kv[k.key(key)]
	return v, ok, nil
}

func (k kv) Set(_ context.Context, key string, value []byte, _ game.MergeRule) error {
	k.r.kv[k.key(key)] = append([]byte(nil), value...)
	return nil
}

func (k kv) Delete(_ context.Context, key string) error {
	delete(k.r.kv, k.key(key))
	return nil
}

type configStore struct{ r *room }

func (c configStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := c.r.config[key]
	if !ok {
		return nil, false, nil
	}
	return []byte(v), true, nil
}

var _ game.Room = (*room)(nil)
