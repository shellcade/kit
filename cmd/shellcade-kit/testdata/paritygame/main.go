// paritygame is the smoke parity fixture: a deterministic per-seat game whose
// frames depend on everything the smoke contract pins — seed (RNG draw),
// virtual clock, per-seat input history, seat identities, styles, graphemes —
// so the native (`go run . -smoke`) and wasm (`shellcade-kit smoke`) paths
// must produce byte-identical shots or the contract is broken.
package main

import (
	"fmt"

	kit "github.com/shellcade/kit/v2"
)

type Game struct{}

func (Game) Meta() kit.GameMeta {
	return kit.GameMeta{Slug: "paritygame", Name: "Parity Game", MinPlayers: 1, MaxPlayers: 4}
}

func (Game) NewRoom(cfg kit.RoomConfig, svc kit.Services) kit.Handler {
	return &room{inputs: map[string]string{}}
}

type room struct {
	kit.Base
	draw   int
	wakes  int
	inputs map[string]string
}

func (h *room) OnStart(r kit.Room) {
	h.draw = r.Rand().Intn(100000)
	h.render(r)
}

func (h *room) OnJoin(r kit.Room, p kit.Player) { h.render(r) }

func (h *room) OnInput(r kit.Room, p kit.Player, in kit.Input) {
	if in.Kind == kit.InputRune {
		h.inputs[p.AccountID] += string(in.Rune)
	} else {
		h.inputs[p.AccountID] += fmt.Sprintf("<%d>", in.Key)
	}
	h.render(r)
}

func (h *room) OnWake(r kit.Room) {
	h.wakes++
	h.render(r)
}

func (h *room) render(r kit.Room) {
	for _, p := range r.Members() {
		f := kit.NewFrame()
		f.Text(0, 0, fmt.Sprintf("%s wakes=%d draw=%d t=%d", p.Handle, h.wakes, h.draw, r.Now().UnixMilli()), kit.Style{FG: kit.Green, Attr: kit.AttrBold})
		f.Text(1, 0, "in="+h.inputs[p.AccountID], kit.Style{FG: kit.RGB(200, 120, 40)})
		f.SetGrapheme(2, 0, "❤️", kit.Style{})
		f.SetWide(2, 4, '個', kit.Style{BG: kit.DimGray})
		r.Send(p, f)
	}
}

func main() { kit.Main(Game{}) }
