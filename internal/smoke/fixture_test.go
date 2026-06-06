package smoke

import (
	"fmt"

	"github.com/shellcade/kit/v2/internal/game"
)

// fixture is a deliberately observable test game: each seat's frame shows the
// inputs it received, the wake count, the room clock, and one RNG draw made
// at start — enough to prove input routing, advance semantics, per-seat
// frames, and determinism. With broadcast=true it sends one Identical frame
// (the broadcast-collapse case).
type fixture struct {
	broadcast bool
}

func (f fixture) Meta() game.GameMeta {
	return game.GameMeta{Slug: "fixture", Name: "Fixture", MinPlayers: 1, MaxPlayers: 4}
}

func (f fixture) NewRoom(cfg game.RoomConfig, svc game.Services) game.Handler {
	return &fixtureRoom{broadcast: f.broadcast, inputs: map[string]string{}}
}

type fixtureRoom struct {
	game.Base
	broadcast bool
	wakes     int
	draw      int
	inputs    map[string]string // accountID -> received input log
}

func (h *fixtureRoom) OnStart(r game.Room) {
	h.draw = r.Rand().Intn(1000)
	h.render(r)
}

func (h *fixtureRoom) OnJoin(r game.Room, p game.Player) { h.render(r) }

func (h *fixtureRoom) OnInput(r game.Room, p game.Player, in game.Input) {
	switch in.Kind {
	case game.InputRune:
		h.inputs[p.AccountID] += string(in.Rune)
	case game.InputKey:
		h.inputs[p.AccountID] += fmt.Sprintf("<%d>", in.Key)
	}
	h.render(r)
}

func (h *fixtureRoom) OnWake(r game.Room) {
	h.wakes++
	h.render(r)
}

func (h *fixtureRoom) render(r game.Room) {
	if h.broadcast {
		f := game.NewFrame()
		f.Text(0, 0, fmt.Sprintf("wakes=%d draw=%d clock=%d", h.wakes, h.draw, r.Now().Unix()), game.Style{})
		r.Identical(f)
		return
	}
	for _, p := range r.Members() {
		f := game.NewFrame()
		f.Text(0, 0, fmt.Sprintf("seat=%s wakes=%d draw=%d clock=%d", p.AccountID, h.wakes, h.draw, r.Now().Unix()), game.Style{})
		f.Text(1, 0, "in="+h.inputs[p.AccountID], game.Style{})
		r.Send(p, f)
	}
}
