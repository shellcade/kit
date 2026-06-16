// loadspike — the BONEYARD Model A load-spike guest. A deliberately
// representative "single resident room" roguelike load: a multi-floor dungeon
// world held in guest memory, every joined player an independent @ with a
// scrolling per-player 80x24 viewport composed and Sent on EVERY wake, wandering
// monsters mutating the world, and bones records littering the floors.
//
// The composition is intentionally the WORST-CASE shape for the wire layer: the
// camera is always centered on the player, so every player movement scrolls the
// whole viewport and produces a keyframe-sized delta (a real game could soften
// this with a camera deadzone — measure first, optimize later).
//
// Steady-state allocation discipline matters because production games build with
// -gc=leaking: the world is allocated once at OnStart, frames are reused, the
// per-wake render path writes cells directly and formats numbers into a fixed
// scratch — no per-player-per-wake heap allocation.
//
// Build (same profile as the fixture / production games):
//
//	tinygo build -opt=1 -no-debug -gc=leaking -o loadspike.wasm \
//	  -target wasip1 -buildmode=c-shared .
package main

import (
	kit "github.com/shellcade/kit/v2"
)

func main() { kit.Main(Game{}) }

const (
	floors  = 12  // dungeon depth (BONEYARD MVP band B1..B12)
	fh      = 40  // floor height (BONEYARD spec floor size)
	fw      = 140 // floor width
	nMon    = 24  // monsters per floor
	nBones  = 12  // rendered bones per floor (BONEYARD render cap)
	mapRows = 22  // viewport rows 0..21; row 22 HUD, row 23 messages
)

type Game struct{}

func (Game) Meta() kit.GameMeta {
	return kit.GameMeta{
		Slug:             "loadspike",
		Name:             "Load Spike",
		ShortDescription: "BONEYARD Model A load-spike guest",
		MinPlayers:       1,
		MaxPlayers:       1000,
		// Large-room callbacks (GUIDE.md "Large rooms"): roster only on
		// change, and the gentle tick this game actually needs.
		CtxFeatures: kit.CtxFeatRosterEpoch,
		HeartbeatMS: 100,
		// The resident lifecycle: this guest doubles as the engine's
		// resident-world testbed (granted in tests, never in production).
		Lifecycle: kit.LifecycleResident,
	}
}

func (Game) NewRoom(cfg kit.RoomConfig, svc kit.Services) kit.Handler {
	return &room{frame: kit.NewFrame()}
}

type mob struct{ x, y int8x2 }

// int8x2 packs a floor-local coordinate pair; fw<256 and fh<256 so uint8 works,
// but keep int16 for arithmetic simplicity under TinyGo.
type int8x2 = int16

type plr struct {
	floor, x, y int
	hp, gold    int
	moves       int
	dirty       bool // re-compose this player's viewport on the next wake
}

type room struct {
	kit.Base
	frame *kit.Frame // one frame reused for every per-player Send

	tiles [floors][fh][fw]byte // '#' wall, '.' open

	monX, monY [floors][nMon]int16
	bonX, bonY [floors][nBones]int16

	players map[string]*plr // keyed by AccountID (hibernation-safe)
	roster  []kit.Player    // join-ordered; maintained on join/leave
	byFloor [floors][]*plr  // rebuilt once per wake for O(floor) viewports

	wakes int
	rng   uint64 // xorshift64 for monster wander (seeded from room RNG)

	numScratch [12]byte // digit scratch for allocation-free HUD numbers
}

// ---- world generation --------------------------------------------------------

func (rm *room) OnStart(r kit.Room) {
	rng := r.Rand() // room-seeded: deterministic world per seed
	rm.players = make(map[string]*plr, 1024)
	rm.rng = uint64(rng.Int63()) | 1

	for f := 0; f < floors; f++ {
		// Random interior walls on a bordered floor...
		for y := 0; y < fh; y++ {
			for x := 0; x < fw; x++ {
				t := byte('.')
				if x == 0 || y == 0 || x == fw-1 || y == fh-1 || rng.Intn(100) < 22 {
					t = '#'
				}
				rm.tiles[f][y][x] = t
			}
		}
		// ...then carve drunkard's-walk corridors so the floor is roamable.
		cx, cy := fw/2, fh/2
		for i := 0; i < 6000; i++ {
			rm.tiles[f][cy][cx] = '.'
			switch rng.Intn(4) {
			case 0:
				if cx < fw-2 {
					cx++
				}
			case 1:
				if cx > 1 {
					cx--
				}
			case 2:
				if cy < fh-2 {
					cy++
				}
			case 3:
				if cy > 1 {
					cy--
				}
			}
		}
		for i := 0; i < nMon; i++ {
			x, y := rm.openTile(rng.Intn, f)
			rm.monX[f][i], rm.monY[f][i] = int16(x), int16(y)
		}
		for i := 0; i < nBones; i++ {
			x, y := rm.openTile(rng.Intn, f)
			rm.bonX[f][i], rm.bonY[f][i] = int16(x), int16(y)
		}
	}
}

// openTile finds a random '.' tile on floor f using the provided intn.
func (rm *room) openTile(intn func(int) int, f int) (int, int) {
	for {
		x, y := 1+intn(fw-2), 1+intn(fh-2)
		if rm.tiles[f][y][x] == '.' {
			return x, y
		}
	}
}

// xorshift64 — allocation-free wander PRNG (determinism of the wander path is
// irrelevant to the spike; the seed still derives from the room RNG).
func (rm *room) next() uint64 {
	x := rm.rng
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	rm.rng = x
	return x
}

// ---- roster ------------------------------------------------------------------

func (rm *room) OnJoin(r kit.Room, p kit.Player) {
	if pl, ok := rm.players[p.AccountID]; ok {
		// Rejoin (same seat, same position) — including post-restore
		// re-seats. The viewer's baselines were invalidated, so they need a
		// frame: render-on-change games MUST dirty the re-seated player or a
		// resumed session stares at nothing (GUIDE.md "Large rooms").
		pl.dirty = true
		return
	}
	f := len(rm.players) % floors
	x, y := rm.openTile(func(n int) int { return int(rm.next() % uint64(n)) }, f)
	rm.players[p.AccountID] = &plr{floor: f, x: x, y: y, hp: 24 + len(rm.players)%17, gold: 0, dirty: true}
	rm.roster = append(rm.roster, p)
	rm.allDirty() // roster change: every viewport re-renders (keyframes anyway)
}

func (rm *room) OnLeave(r kit.Room, p kit.Player) {
	delete(rm.players, p.AccountID)
	for i := range rm.roster {
		if rm.roster[i].AccountID == p.AccountID {
			rm.roster = append(rm.roster[:i], rm.roster[i+1:]...)
			break
		}
	}
	rm.allDirty()
}

func (rm *room) allDirty() {
	for _, pl := range rm.players {
		pl.dirty = true
	}
}

// ---- input -------------------------------------------------------------------

func (rm *room) OnInput(r kit.Room, p kit.Player, in kit.Input) {
	pl, ok := rm.players[p.AccountID]
	if !ok {
		return
	}
	dx, dy := 0, 0
	if in.Kind == kit.InputRune {
		switch in.Rune {
		case 'h':
			dx = -1
		case 'l':
			dx = 1
		case 'k':
			dy = -1
		case 'j':
			dy = 1
		case 'b': // bones interaction stand-in
			pl.gold++
			return
		default:
			return
		}
	} else if in.Kind == kit.InputKey {
		switch in.Key {
		case kit.KeyUp:
			dy = -1
		case kit.KeyDown:
			dy = 1
		case kit.KeyLeft:
			dx = -1
		case kit.KeyRight:
			dx = 1
		default:
			return
		}
	}
	nx, ny := pl.x+dx, pl.y+dy
	if nx >= 0 && nx < fw && ny >= 0 && ny < fh && rm.tiles[pl.floor][ny][nx] == '.' {
		pl.x, pl.y = nx, ny
		pl.moves++
		pl.dirty = true
		// Same-floor players with the mover inside their viewport see the
		// @ move — their views are dirty too (render-on-change rule 2).
		rm.dirtyWitnesses(pl.floor, nx, ny, pl)
	}
}

// dirtyWitnesses marks players on floor f dirty when world cell (x,y) is
// inside their (clamped, centered) viewport.
func (rm *room) dirtyWitnesses(f, x, y int, except *plr) {
	for _, o := range rm.byFloor[f] {
		if o == except || o.dirty {
			continue
		}
		ox := clamp(o.x-kit.Cols/2, 0, fw-kit.Cols)
		oy := clamp(o.y-mapRows/2, 0, fh-mapRows)
		if x >= ox && x < ox+kit.Cols && y >= oy && y < oy+mapRows {
			o.dirty = true
		}
	}
}

// ---- wake: simulate + compose + fan out ---------------------------------------

var (
	stWall  = kit.Style{FG: kit.DimGray}
	stFloor = kit.Style{FG: kit.Gray(0x40)}
	stBones = kit.Style{FG: kit.White}
	stMon   = kit.Style{FG: kit.Red}
	stOther = kit.Style{FG: kit.Cyan}
	stSelf  = kit.Style{FG: kit.White, Attr: kit.AttrBold}
	stHUD   = kit.Style{FG: kit.Yellow}
)

func (rm *room) OnWake(r kit.Room) {
	rm.wakes++

	// Rebuild the per-floor player index once (O(N)), so witness checks and
	// viewports scan only their own floor's occupants.
	for f := range rm.byFloor {
		rm.byFloor[f] = rm.byFloor[f][:0]
	}
	for _, pl := range rm.players {
		rm.byFloor[pl.floor] = append(rm.byFloor[pl.floor], pl)
	}

	// Monsters wander every 4th wake (the BONEYARD "gentle tick"); each move
	// dirties only the viewports that can see it (render-on-change rule 2).
	if rm.wakes%4 == 0 {
		for f := 0; f < floors; f++ {
			for i := 0; i < nMon; i++ {
				x, y := int(rm.monX[f][i]), int(rm.monY[f][i])
				switch rm.next() % 4 {
				case 0:
					x++
				case 1:
					x--
				case 2:
					y++
				case 3:
					y--
				}
				if x > 0 && x < fw-1 && y > 0 && y < fh-1 && rm.tiles[f][y][x] == '.' {
					rm.monX[f][i], rm.monY[f][i] = int16(x), int16(y)
					rm.dirtyWitnesses(f, x, y, nil)
				}
			}
		}
	}

	// Ambient HUD clock at ~1Hz, not per-wake (render-on-change rule 3): a
	// per-wake counter would force a nonzero delta for every player on every
	// wake, defeating both the dirty tracking and the delta encoder.
	if rm.wakes%20 == 0 {
		rm.allDirty()
	}

	// Per-player viewport composition + Send — DIRTY VIEWS ONLY.
	for _, p := range rm.roster {
		pl, ok := rm.players[p.AccountID]
		if !ok || !pl.dirty {
			continue
		}
		pl.dirty = false
		rm.compose(pl)
		r.Send(p, rm.frame)
	}
}

// compose renders pl's centered viewport into rm.frame (reused across players;
// every cell of the map area is overwritten so no Clear is needed for rows
// 0..21; HUD rows are fully rewritten too).
func (rm *room) compose(pl *plr) {
	f := rm.frame
	fl := pl.floor

	// Camera: centered on the player, clamped to floor bounds. Centered camera
	// = every move scrolls the viewport = worst-case deltas (intentional).
	ox := clamp(pl.x-kit.Cols/2, 0, fw-kit.Cols)
	oy := clamp(pl.y-mapRows/2, 0, fh-mapRows)

	for vy := 0; vy < mapRows; vy++ {
		wy := oy + vy
		rowTiles := &rm.tiles[fl][wy]
		for vx := 0; vx < kit.Cols; vx++ {
			t := rowTiles[ox+vx]
			if t == '#' {
				f.Cells[vy][vx] = kit.Cell{Rune: '#', FG: cellFG(stWall)}
			} else {
				f.Cells[vy][vx] = kit.Cell{Rune: '.', FG: cellFG(stFloor)}
			}
		}
	}

	// Bones, monsters, players on this floor (plot if inside the window).
	for i := 0; i < nBones; i++ {
		rm.plot(int(rm.bonX[fl][i]), int(rm.bonY[fl][i]), ox, oy, '†', stBones)
	}
	for i := 0; i < nMon; i++ {
		rm.plot(int(rm.monX[fl][i]), int(rm.monY[fl][i]), ox, oy, 'k', stMon)
	}
	for _, other := range rm.byFloor[fl] {
		if other != pl {
			rm.plot(other.x, other.y, ox, oy, '@', stOther)
		}
	}
	rm.plot(pl.x, pl.y, ox, oy, '@', stSelf)

	// HUD row 22: HP, floor, gold, wake counter (the counter guarantees a
	// nonzero delta every wake even for idle players — like a real game's
	// clock/torch readout).
	for vx := 0; vx < kit.Cols; vx++ {
		f.Cells[22][vx] = kit.Cell{}
	}
	f.Text(22, 0, "HP", stHUD)
	rm.putNum(22, 3, pl.hp)
	f.Text(22, 8, "B", stHUD)
	rm.putNum(22, 9, fl+1)
	f.Text(22, 14, "$", stHUD)
	rm.putNum(22, 15, pl.gold)
	f.Text(22, 24, "t", stHUD)
	rm.putNum(22, 25, rm.wakes/20) // ~1Hz ambient clock (HUD throttle)

	// Message row 23: static hint (cheap, rarely changes).
	for vx := 0; vx < kit.Cols; vx++ {
		f.Cells[23][vx] = kit.Cell{}
	}
	f.Text(23, 0, "[hjkl]move [b]bones", kit.Style{FG: kit.DimGray})
}

// plot writes a world-coordinate glyph into the viewport if visible.
func (rm *room) plot(wx, wy, ox, oy int, g rune, st kit.Style) {
	vx, vy := wx-ox, wy-oy
	if vx >= 0 && vx < kit.Cols && vy >= 0 && vy < mapRows {
		rm.frame.Cells[vy][vx] = kit.Cell{Rune: g, FG: cellFG(st), Attr: st.Attr}
	}
}

// putNum writes n's decimal digits at (row,col) without allocating.
func (rm *room) putNum(row, col, n int) {
	i := len(rm.numScratch)
	if n == 0 {
		i--
		rm.numScratch[i] = '0'
	}
	for n > 0 && i > 0 {
		i--
		rm.numScratch[i] = byte('0' + n%10)
		n /= 10
	}
	for j := i; j < len(rm.numScratch); j++ {
		rm.frame.Cells[row][col+(j-i)] = kit.Cell{Rune: rune(rm.numScratch[j]), FG: cellFG(stHUD)}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// cellFG extracts the style FG for direct Cell writes.
func cellFG(st kit.Style) kit.Color { return st.FG }
