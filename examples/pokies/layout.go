package main

import (
	"fmt"

	gamekit "github.com/shellcade/gamekit"
)

// Rendering: a near-verbatim port of the native pokies layout to the gamekit
// grid. Five 15-wide cabinets plus gutters fit inside 80 cols; each cabinet
// frames a 3x3 reel window whose center row is the payline.
const (
	cardW       = 15
	cardH       = 11
	gutter      = 1
	cardTop     = 3
	maxMachines = 5
	scrollSpeed = 2
)

var (
	stTitle   = gamekit.Style{FG: gamekit.White, Attr: gamekit.AttrBold}
	stDim     = gamekit.Style{FG: gamekit.DimGray}
	stTicker  = gamekit.Style{FG: gamekit.Yellow, Attr: gamekit.AttrBold}
	stBordOwn = gamekit.Style{FG: gamekit.Cyan, Attr: gamekit.AttrBold}
	stBordDim = gamekit.Style{FG: gamekit.DimGray}
	stNameOwn = gamekit.Style{FG: gamekit.Yellow, Attr: gamekit.AttrBold}
	stName    = gamekit.Style{FG: gamekit.White}
	stPayline = gamekit.Style{FG: gamekit.Yellow, Attr: gamekit.AttrBold}
	stReelDim = gamekit.Style{FG: gamekit.DimGray}
	stMarker  = gamekit.Style{FG: gamekit.Cyan, Attr: gamekit.AttrBold}
	stLabel   = gamekit.Style{FG: gamekit.DimGray}
	stWin     = gamekit.Style{FG: gamekit.Green, Attr: gamekit.AttrBold}
	stRebuy   = gamekit.Style{FG: gamekit.Red, Attr: gamekit.AttrBold}
	stReady   = gamekit.Style{FG: gamekit.DimGray}
	stLever   = gamekit.Style{FG: gamekit.Red, Attr: gamekit.AttrBold}
)

// render composes and sends a per-viewer frame to every member.
func (rm *room) render(r gamekit.Room) {
	rm.lastNow = r.Now()
	for _, v := range r.Members() {
		r.Send(v, rm.compose(v))
	}
}

func (rm *room) compose(v gamekit.Player) *gamekit.Frame {
	f := gamekit.NewFrame()

	f.Text(0, 2, "*** POKIES ***", stTitle)
	f.TextRight(0, gamekit.Cols-2, "pull the lever - chase your high score", stDim)

	if rm.tickerActive(rm.lastNow) {
		msg := "* " + rm.ticker.text + " *"
		f.Text(1, (gamekit.Cols-len(msg))/2, msg, stTicker)
	}

	n := len(rm.order)
	if n > maxMachines {
		n = maxMachines
	}
	if n > 0 {
		group := n*cardW + (n-1)*gutter
		start := (gamekit.Cols - group) / 2
		for i := 0; i < n; i++ {
			id := rm.order[i]
			rm.drawCard(f, start+i*(cardW+gutter), cardTop, id, id == v.AccountID)
		}
	}

	f.Text(gamekit.Rows-1, 2, "Up/Down bet   SPACE spin   Esc leave", stDim)
	if m := rm.machines[v.AccountID]; m != nil {
		f.TextRight(gamekit.Rows-1, gamekit.Cols-2, fmt.Sprintf("BAL %d   HI %d", m.balance, m.highScore), stDim)
	}
	return f
}

func (rm *room) drawCard(f *gamekit.Frame, col, top int, id string, own bool) {
	m := rm.machines[id]
	if m == nil {
		return
	}
	bord, nameSt := stBordDim, stName
	if own {
		bord, nameSt = stBordOwn, stNameOwn
	}
	left, right := col, col+cardW-1

	rm.border(f, top, col, '╭', '╮', bord)
	name := id
	if p, ok := rm.names[id]; ok {
		name = p.Handle
	}
	if maxName := cardW - 4; len(name) > maxName {
		name = name[:maxName]
	}
	f.Text(top, col+2, " "+name+" ", nameSt)

	for r := 1; r <= 9; r++ {
		f.SetRune(top+r, left, '│', bord)
		f.SetRune(top+r, right, '│', bord)
	}

	sx := col + 2
	f.SetRune(top+1, sx, '╭', bord)
	f.SetRune(top+5, sx, '╰', bord)
	f.SetRune(top+1, sx+6, '╮', bord)
	f.SetRune(top+5, sx+6, '╯', bord)
	for c := sx + 1; c < sx+6; c++ {
		f.SetRune(top+1, c, '─', bord)
		f.SetRune(top+5, c, '─', bord)
	}
	for r := 2; r <= 4; r++ {
		f.SetRune(top+r, sx, '│', bord)
		f.SetRune(top+r, sx+6, '│', bord)
	}

	g := rm.grid(m)
	for row := 0; row < 3; row++ {
		st := stReelDim
		if row == 1 {
			st = stPayline
		}
		for reel := 0; reel < 3; reel++ {
			f.SetRune(top+2+row, sx+1+reel*2, g[row][reel], st)
		}
	}
	f.SetRune(top+3, sx-1, '>', stMarker)
	f.SetRune(top+3, sx+7, '<', stMarker)

	rm.lever(f, col, top, m)

	rm.body(f, top+6, col, "HI", m.highScore)
	rm.body(f, top+7, col, "BAL", m.balance)
	rm.body(f, top+8, col, "BET", m.bet)
	rm.status(f, top+9, col, m)

	rm.border(f, top+10, col, '╰', '╯', bord)
	f.Text(top+10, col+5, "[__]", bord)
}

func (rm *room) border(f *gamekit.Frame, row, col int, lc, rc rune, st gamekit.Style) {
	f.SetRune(row, col, lc, st)
	f.SetRune(row, col+cardW-1, rc, st)
	for c := col + 1; c < col+cardW-1; c++ {
		f.SetRune(row, c, '─', st)
	}
}

func (rm *room) body(f *gamekit.Frame, row, col int, label string, val int) {
	f.Text(row, col+2, label, stLabel)
	f.TextRight(row, col+cardW-2, fmt.Sprintf("%d", val), stTitle)
}

func (rm *room) status(f *gamekit.Frame, row, col int, m *machine) {
	text, st := "ready", stReady
	switch {
	case m.flash == "RE-BUY":
		text, st = m.flash, stRebuy
	case m.flash != "":
		text, st = m.flash, stWin
	case m.spin != nil:
		text, st = "spinning", stReady
	}
	if len(text) > cardW-2 {
		text = text[:cardW-2]
	}
	f.Text(row, col+(cardW-len(text))/2, text, st)
}

func (rm *room) lever(f *gamekit.Frame, col, top int, m *machine) {
	lx := col + 11
	knob := top + 1
	if m.spin != nil {
		knob = top + 3
	}
	for r := 1; r <= 4; r++ {
		ch := '│'
		if top+r == knob {
			ch = 'O'
		}
		f.SetRune(top+r, lx, ch, stLever)
	}
	f.SetRune(top+5, lx, '┴', stLever)
}

// grid returns the 3x3 visible faces. Reels scroll while spinning (cycle is
// derived from elapsed time), freeze as they land, show the last result when
// idle, and are blank before the first spin.
func (rm *room) grid(m *machine) [3][3]rune {
	var out [3][3]rune
	for reel := 0; reel < 3; reel++ {
		var w [3]symbol
		switch {
		case m.spin != nil && reel >= m.spin.landed:
			w = rollWindow(spinStrip(m.spin), m.spin.cycle(rm.lastNow)*scrollSpeed+reel*7)
		case m.spin != nil:
			w = windowAt(spinStrip(m.spin), m.spin.stopIdx[reel])
		case m.spun:
			w = windowAt(idleStrip(m), m.lastIdx[reel])
		default:
			w = [3]symbol{symBlank, symBlank, symBlank}
		}
		for row := 0; row < 3; row++ {
			out[row][reel] = rune(w[row])
		}
	}
	return out
}

func spinStrip(s *spinState) []symbol {
	if s.variant != nil && len(s.variant.strip) > 0 {
		return s.variant.strip
	}
	return defaultVariant().strip
}

func idleStrip(m *machine) []symbol {
	if len(m.lastStrip) > 0 {
		return m.lastStrip
	}
	return defaultVariant().strip
}
