package sdk

import (
	"testing"
	"time"
)

// ctxHandler publishes a given InputContext on join.
type ctxHandler struct {
	Base
	ctx InputContext
}

func (h ctxHandler) OnJoin(r Room, _ Player) { r.SetInputContext(h.ctx) }

func TestRoomCtlInputContextDefaultsToNav(t *testing.T) {
	ctl := NewRoomRuntime("r-default", Base{}, RoomConfig{}, Services{})
	defer ctl.Close()
	if got := ctl.InputContext(); got != CtxNav {
		t.Fatalf("default InputContext = %v, want CtxNav", got)
	}
}

func TestRoomCtlInputContextPublished(t *testing.T) {
	ctl := NewRoomRuntime("r-pub", ctxHandler{ctx: CtxText}, RoomConfig{Capacity: 1}, Services{})
	defer ctl.Close()
	if err := ctl.Join(Player{Conn: "c1"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	// The actor processes OnJoin asynchronously; poll briefly for publication.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ctl.InputContext() == CtxText {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("InputContext = %v, want CtxText after game published it", ctl.InputContext())
}

func TestResolveNav(t *testing.T) {
	cases := []struct {
		in   Input
		want Action
	}{
		{KeyInput(KeyUp), ActUp},
		{RuneInput('k'), ActUp},
		{KeyInput(KeyDown), ActDown},
		{RuneInput('j'), ActDown},
		{KeyInput(KeyLeft), ActLeft},
		{RuneInput('h'), ActLeft},
		{KeyInput(KeyRight), ActRight},
		{RuneInput('l'), ActRight},
		{KeyInput(KeyEnter), ActConfirm},
		{RuneInput(' '), ActConfirm},
		{KeyInput(KeyEsc), ActBack},
		{KeyInput(KeyCtrlC), ActBack},
		{RuneInput('q'), ActBack},
		{RuneInput('x'), ActNone},
		{KeyInput(KeyBackspace), ActNone},
	}
	for _, c := range cases {
		if got := Resolve(c.in, CtxNav); got != c.want {
			t.Errorf("Resolve(%+v, CtxNav) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestResolveCommand(t *testing.T) {
	// Arrows still navigate; Enter/Space confirm; Esc/Ctrl-C/q back out.
	nav := []struct {
		in   Input
		want Action
	}{
		{KeyInput(KeyUp), ActUp},
		{KeyInput(KeyDown), ActDown},
		{KeyInput(KeyLeft), ActLeft},
		{KeyInput(KeyRight), ActRight},
		{KeyInput(KeyEnter), ActConfirm},
		{RuneInput(' '), ActConfirm},
		{KeyInput(KeyEsc), ActBack},
		{KeyInput(KeyCtrlC), ActBack},
		{RuneInput('q'), ActBack},
	}
	for _, c := range nav {
		if got := Resolve(c.in, CtxCommand); got != c.want {
			t.Errorf("Resolve(%+v, CtxCommand) = %v, want %v", c.in, got, c.want)
		}
	}
	// Letter aliases are domain commands here, NOT directions.
	for _, r := range []rune{'h', 'j', 'k', 'l', 's', 'd', 'p', 'r', 'y', 'n'} {
		if got := Resolve(RuneInput(r), CtxCommand); got != ActNone {
			t.Errorf("Resolve(%q, CtxCommand) = %v, want ActNone (domain command)", r, got)
		}
	}
}

func TestResolveText(t *testing.T) {
	// Only Esc/Ctrl-C resolve; every printable rune (incl. q/j/k) passes raw.
	if got := Resolve(KeyInput(KeyEsc), CtxText); got != ActBack {
		t.Errorf("Resolve(Esc, CtxText) = %v, want ActBack", got)
	}
	if got := Resolve(KeyInput(KeyCtrlC), CtxText); got != ActBack {
		t.Errorf("Resolve(Ctrl-C, CtxText) = %v, want ActBack", got)
	}
	for _, in := range []Input{
		RuneInput('q'), RuneInput('j'), RuneInput('k'), RuneInput('a'),
		KeyInput(KeyEnter), KeyInput(KeyBackspace), KeyInput(KeyUp),
	} {
		if got := Resolve(in, CtxText); got != ActNone {
			t.Errorf("Resolve(%+v, CtxText) = %v, want ActNone", in, got)
		}
	}
}
