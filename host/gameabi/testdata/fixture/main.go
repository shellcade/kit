// fixture — the gameabi test guest. Renders a tiny deterministic status frame
// and misbehaves (or exercises an ABI surface) on command, so host tests can
// prove containment, derived joinability, and the virtualized WASI story
// against a real artifact:
//
//	'p' panic (guest trap)         'l' spin forever (callback deadline)
//	'o' allocate forever (linear-memory cap)
//	'e' end the room (winner = sender, metric 42)
//	's' post a leaderboard result (metric 7)
//	'k' kv round-trip on the sender's store, logged
//	'i' cycle the input context (nav -> command -> text -> nav)
//	'f' send a PERSONAL frame to the inputting player only (distinct content)
//	'c' config_get("greeting"), logged
//	't' log the guest's own time.Now().UnixNano() (== the room clock)
//	'r' log 8 bytes from crypto/rand (the WASI-virtualized entropy source)
//	'd' arm a 250ms countdown deadline; each wake renders remaining ms, then
//	    when CallContext time passes the deadline it renders BOOM and ends
//
// Build (dev profile — `make fixture-wasm` from the repo root):
//
//	tinygo build -o fixture.wasm -opt=1 -no-debug -gc=leaking \
//	  -target wasip1 -buildmode=c-shared .
package main

import (
	"context"
	crand "crypto/rand"
	"strconv"

	kit "github.com/shellcade/kit/v2"
)

func main() { kit.Main(Game{}) }

// Game is the fixture registry entry.
type Game struct{}

func (Game) Meta() kit.GameMeta {
	return kit.GameMeta{
		Slug:             "fixture",
		Name:             "Fixture",
		ShortDescription: "gameabi test guest",
		MinPlayers:       1,
		MaxPlayers:       2,
	}
}

func (Game) NewRoom(cfg kit.RoomConfig, svc kit.Services) kit.Handler {
	return &room{frame: kit.NewFrame(), pframe: kit.NewFrame()}
}

// Globals keep the misbehavior loops observable so the optimizer cannot drop
// them (gc=leaking: 'o' grows linear memory until the cap traps the instance).
var (
	sink [][]byte
	spin uint64
)

// countdownMs is the 'd' command's armed window. Held in guest memory across
// wakes so the host can drive the deadline purely via wake + CallContext time.
const countdownMs = 250

type room struct {
	kit.Base
	frame  *kit.Frame // reused per render (allocation-free steady state)
	pframe *kit.Frame // reused for personal ('f') frames
	wakes  int

	ctxIdx int // 'i' cycle position

	// 'd' countdown: deadlineAt is the room-clock nanos at which the room ends.
	// armed is false until 'd' is pressed; once armed, every wake renders the
	// remaining ms (or BOOM + End once the clock passes the deadline).
	armed      bool
	deadlineAt int64

	entropy [8]byte // 'r' scratch (allocation-free crypto/rand read)
}

func (rm *room) OnStart(r kit.Room)               { rm.render(r) }
func (rm *room) OnJoin(r kit.Room, p kit.Player)  { rm.render(r) }
func (rm *room) OnLeave(r kit.Room, p kit.Player) { rm.render(r) }

func (rm *room) OnWake(r kit.Room) {
	rm.wakes++
	if rm.armed {
		rm.tickCountdown(r)
		return
	}
	rm.render(r)
}

func (rm *room) OnInput(r kit.Room, p kit.Player, in kit.Input) {
	if in.Kind != kit.InputRune {
		return
	}
	switch in.Rune {
	case 'p':
		panic("fixture: deliberate panic")
	case 'l':
		for { // burn until the host's callback deadline kills the instance
			spin++
			if spin == 0 {
				r.Log("fixture: spin wrapped")
			}
		}
	case 'o':
		for { // allocate past the linear-memory cap
			sink = append(sink, make([]byte, 1<<20))
		}
	case 'e':
		r.End(kit.Result{Rankings: []kit.PlayerResult{{Player: p, Metric: 42, Rank: 1}}})
	case 's':
		r.Post(kit.Result{Rankings: []kit.PlayerResult{{Player: p, Metric: 7, Rank: 1}}})
	case 'k':
		store := r.Services().Accounts.For(p).Store()
		_ = store.Set(context.Background(), "visits", []byte("1"), kit.MergeSum)
		if v, ok, _ := store.Get(context.Background(), "visits"); ok {
			r.Log("fixture: visits=" + string(v))
		}
	case 'i':
		// Cycle nav -> command -> text -> nav, publishing each.
		ctxs := [...]kit.InputContext{kit.CtxNav, kit.CtxCommand, kit.CtxText}
		rm.ctxIdx = (rm.ctxIdx + 1) % len(ctxs)
		r.SetInputContext(ctxs[rm.ctxIdx])
		r.Log("fixture: ctx=" + strconv.Itoa(int(ctxs[rm.ctxIdx])))
	case 'f':
		// A PERSONAL frame to the inputting player only — distinct content from
		// the broadcast banner so the host can tell them apart.
		f := rm.pframe
		f.Clear()
		f.Text(0, 0, "PERSONAL", kit.Style{})
		f.Text(1, 0, "you="+p.Handle, kit.Style{})
		r.Send(p, f)
	case 'c':
		v, ok, _ := r.Services().Config.Get(context.Background(), "greeting")
		if ok {
			r.Log("fixture: greeting=" + string(v))
		} else {
			r.Log("fixture: greeting=<unset>")
		}
	case 't':
		// The guest's own clock — virtualized to the room clock by the host, so
		// this nanos value equals the callback's CallContext time.
		r.Log("fixture: now=" + strconv.FormatInt(r.Now().UnixNano(), 10))
	case 'r':
		// Read 8 bytes from crypto/rand: in wasip1 this hits the WASI
		// random_get the host virtualizes with the room-seeded source, so two
		// rooms with the same seed log identical entropy.
		_, _ = crand.Read(rm.entropy[:])
		r.Log("fixture: rand=" + hex8(rm.entropy))
	case 'd':
		// Arm a countdown anchored to the room clock; the host drives it forward
		// with wakes (CallContext time), no host-side timer involved.
		rm.armed = true
		rm.deadlineAt = r.Now().UnixNano() + countdownMs*1_000_000
		rm.tickCountdown(r)
	default:
		rm.render(r)
	}
}

// tickCountdown renders the remaining ms until the armed deadline, or BOOM + End
// once the room clock has passed it. Time is read from the CallContext only.
func (rm *room) tickCountdown(r kit.Room) {
	rem := (rm.deadlineAt - r.Now().UnixNano()) / 1_000_000
	f := rm.frame
	f.Clear()
	if rem <= 0 {
		f.Text(0, 0, "BOOM", kit.Style{})
		r.Identical(f)
		r.End(kit.Result{})
		return
	}
	f.Text(0, 0, "COUNTDOWN", kit.Style{})
	f.Text(1, 0, "remaining_ms="+strconv.FormatInt(rem, 10), kit.Style{})
	r.Identical(f)
}

func (rm *room) render(r kit.Room) {
	f := rm.frame
	f.Clear()
	f.Text(0, 0, "FIXTURE", kit.Style{})
	f.Text(1, 0, "players="+strconv.Itoa(r.Count()), kit.Style{})
	f.Text(2, 0, "wakes="+strconv.Itoa(rm.wakes), kit.Style{})
	r.Identical(f)
}

// hex8 formats 8 bytes as lowercase hex without allocating a slice per call
// (a fixed-size scratch array keeps the 'r' path allocation-light).
func hex8(b [8]byte) string {
	const digits = "0123456789abcdef"
	var out [16]byte
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0f]
	}
	return string(out[:])
}
