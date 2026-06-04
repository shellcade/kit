//go:build !wasip1 && !tinygo.wasm

package gamekit

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Main, built natively (not wasm), is the instant inner-loop dev runner:
// `go run .` in a game directory plays the game in this terminal with normal
// Go tooling (debugger, prints, sub-second rebuilds) and zero wasm involved.
// The wasm artifact is verified separately by `devkit check` — including the
// determinism check that guarantees the two backends behave identically.
//
// Flags: -seed N · -heartbeat 50ms · -config k=v (repeatable, v may be @file)
// · -handle name. Esc or Ctrl-C leaves.
func Main(g Game) {
	seed := flag.Int64("seed", 0, "room RNG seed (0 = time-based)")
	heartbeat := flag.Duration("heartbeat", 50*time.Millisecond, "wake cadence")
	handle := flag.String("handle", "dev", "player handle (seat 1)")
	seats := flag.Int("seats", 1, "players joined to the room; Ctrl-T switches the active seat")
	cfgVals := map[string]string{}
	flag.Var(cfgFlag(cfgVals), "config", "KEY=VALUE per-game config (repeatable; value may be @file)")
	flag.Parse()

	s := *seed
	if s == 0 {
		s = time.Now().UnixNano()
	}
	meta := g.Meta()
	cfg := RoomConfig{Mode: ModeSolo, Capacity: meta.MaxPlayers, MinPlayers: meta.MinPlayers, Seed: s, SeedSet: true}
	if *seats < 1 {
		*seats = 1
	}
	if *seats > meta.MaxPlayers {
		*seats = meta.MaxPlayers
	}
	players := make([]Player, *seats)
	for i := range players {
		h := fmt.Sprintf("seat%d", i+1)
		if i == 0 {
			h = *handle
		}
		players[i] = Player{AccountID: fmt.Sprintf("seat-%d", i+1), Handle: h, Kind: KindMember, Conn: fmt.Sprintf("local-%d", i+1)}
	}

	r := &nativeRoom{
		cfg:     cfg,
		members: []Player{},
		rng:     rand.New(rand.NewSource(s)),
		kv:      map[string][]byte{},
		config:  cfgVals,
	}
	h := g.NewRoom(cfg, r.Services())

	// Terminal: raw mode, alt screen, hidden cursor.
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gamekit: need a real terminal:", err)
		os.Exit(1)
	}
	defer func() {
		_ = term.Restore(fd, state)
		fmt.Print("\x1b[?25h\x1b[?1049l")
	}()
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[2J")

	h.OnStart(r)
	for i, p := range players {
		r.members = players[:i+1]
		h.OnJoin(r, p)
	}
	r.members = players

	keys := make(chan Input, 16)
	done := make(chan struct{})
	r.seatSwitch = make(chan struct{}, 4)
	seatSwitches = r.seatSwitch
	go readKeys(keys, done)

	tick := time.NewTicker(*heartbeat)
	defer tick.Stop()
	for !r.ended {
		select {
		case <-tick.C:
			h.OnWake(r)
		case in, ok := <-keys:
			if !ok {
				leaveAll(h, r, players)
				return
			}
			h.OnInput(r, players[r.active], in)
		case <-r.seatSwitch:
			r.active = (r.active + 1) % len(players)
		case <-done:
			leaveAll(h, r, players)
			return
		}
	}
	h.OnClose(r)
}

type cfgFlag map[string]string

func (c cfgFlag) String() string { return "" }
func (c cfgFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("-config wants KEY=VALUE, got %q", v)
	}
	if strings.HasPrefix(val, "@") {
		b, err := os.ReadFile(val[1:])
		if err != nil {
			return err
		}
		val = string(b)
	}
	c[k] = val
	return nil
}

// leaveAll delivers leaves for every seat (last leave first-joined) and closes.
func leaveAll(h Handler, r *nativeRoom, players []Player) {
	for i := len(players) - 1; i >= 0; i-- {
		r.members = players[:i]
		h.OnLeave(r, players[i])
	}
	h.OnClose(r)
}

// readKeys parses raw stdin bytes into Inputs; closes done on Esc/Ctrl-C.
func readKeys(out chan<- Input, done chan<- struct{}) {
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			close(done)
			return
		}
		i := 0
		for i < n {
			b := buf[i]
			switch {
			case b == 0x03: // Ctrl-C
				close(done)
				return
			case b == 0x14: // Ctrl-T: next seat
				seatSwitches <- struct{}{}
			case b == 0x1b:
				if i+2 < n && buf[i+1] == '[' {
					switch buf[i+2] {
					case 'A':
						out <- Input{Kind: InputKey, Key: KeyUp}
					case 'B':
						out <- Input{Kind: InputKey, Key: KeyDown}
					case 'C':
						out <- Input{Kind: InputKey, Key: KeyRight}
					case 'D':
						out <- Input{Kind: InputKey, Key: KeyLeft}
					}
					i += 3
					continue
				}
				close(done) // bare Esc: leave
				return
			case b == '\r' || b == '\n':
				out <- Input{Kind: InputKey, Key: KeyEnter}
			case b == 0x7f:
				out <- Input{Kind: InputKey, Key: KeyBackspace}
			case b == '\t':
				out <- Input{Kind: InputKey, Key: KeyTab}
			case b >= 0x20:
				out <- Input{Kind: InputRune, Rune: rune(b)}
			}
			i++
		}
	}
}

// seatSwitches carries Ctrl-T presses from the key reader to the run loop.
var seatSwitches chan<- struct{}

// nativeRoom implements Room directly against the terminal + in-memory state.
type nativeRoom struct {
	cfg        RoomConfig
	members    []Player
	active     int // hot-seat: which member the keyboard controls / renders
	seatSwitch chan struct{}
	rng        *rand.Rand
	kv         map[string][]byte // keyed by account + key
	config     map[string]string
	ended      bool
}

func (r *nativeRoom) Members() []Player  { return r.members }
func (r *nativeRoom) Count() int         { return len(r.members) }
func (r *nativeRoom) Config() RoomConfig { return r.cfg }
func (r *nativeRoom) Rand() *rand.Rand   { return r.rng }
func (r *nativeRoom) Now() time.Time     { return time.Now() }
func (r *nativeRoom) Settled() bool      { return r.ended }

func (r *nativeRoom) Has(p Player) bool {
	for _, m := range r.members {
		if m == p {
			return true
		}
	}
	return false
}

func (r *nativeRoom) Send(p Player, f *Frame) {
	if f == nil || r.active >= len(r.members) || p != r.members[r.active] {
		return // render only the active seat's view
	}
	out := "\x1b[H" + frameToANSI(f)
	if len(r.members) > 1 {
		out += fmt.Sprintf("\r\n\x1b[2m seat %d/%d — Ctrl-T switches \x1b[0m", r.active+1, len(r.members))
	}
	os.Stdout.WriteString(out)
}

func (r *nativeRoom) Identical(f *Frame) {
	if r.active < len(r.members) {
		r.Send(r.members[r.active], f)
	}
}

func (r *nativeRoom) SetInputContext(InputContext) {}
func (r *nativeRoom) End(Result)                   { r.ended = true }
func (r *nativeRoom) Post(Result)                  {}
func (r *nativeRoom) Log(msg string)               { fmt.Fprintln(os.Stderr, "\r"+msg) }

func (r *nativeRoom) Services() Services {
	return Services{Accounts: nativeAccounts{r}, Config: nativeConfig{r}}
}

type nativeAccounts struct{ r *nativeRoom }

func (a nativeAccounts) For(p Player) Account { return nativeAccount{a.r, p} }

type nativeAccount struct {
	r *nativeRoom
	p Player
}

func (a nativeAccount) ID() string     { return a.p.AccountID }
func (a nativeAccount) Handle() string { return a.p.Handle }
func (a nativeAccount) Kind() Kind     { return a.p.Kind }
func (a nativeAccount) Store() KVStore { return nativeKV{a.r, a.p.AccountID} }

type nativeKV struct {
	r       *nativeRoom
	account string
}

func (k nativeKV) key(s string) string { return k.account + "\x00" + s }

func (k nativeKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := k.r.kv[k.key(key)]
	return v, ok, nil
}

func (k nativeKV) Set(_ context.Context, key string, value []byte, _ MergeRule) error {
	k.r.kv[k.key(key)] = append([]byte(nil), value...)
	return nil
}

func (k nativeKV) Delete(_ context.Context, key string) error {
	delete(k.r.kv, k.key(key))
	return nil
}

type nativeConfig struct{ r *nativeRoom }

func (c nativeConfig) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := c.r.config[key]
	if !ok {
		return nil, false, nil
	}
	return []byte(v), true, nil
}

// frameToANSI is a minimal truecolor encoder for the dev runner (the arcade's
// renderer of record lives host-side; this only needs to look right locally).
func frameToANSI(f *Frame) string {
	var b strings.Builder
	b.Grow(Rows * Cols * 8)
	for row := 0; row < Rows; row++ {
		last := ""
		for col := 0; col < Cols; col++ {
			c := f.Cells[row][col]
			if c.Cont {
				continue
			}
			sgr := cellSGR(c)
			if sgr != last {
				b.WriteString(sgr)
				last = sgr
			}
			ru := c.Rune
			if ru == 0 || ru < 0x20 {
				ru = ' '
			}
			b.WriteRune(ru)
		}
		b.WriteString("\x1b[0m")
		if row < Rows-1 {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func cellSGR(c Cell) string {
	var parts []string
	parts = append(parts, "0")
	if c.Attr&AttrBold != 0 {
		parts = append(parts, "1")
	}
	if c.Attr&AttrDim != 0 {
		parts = append(parts, "2")
	}
	if c.Attr&AttrUnderline != 0 {
		parts = append(parts, "4")
	}
	if c.Attr&AttrReverse != 0 {
		parts = append(parts, "7")
	}
	if c.FG.set {
		parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", c.FG.r, c.FG.g, c.FG.b))
	}
	if c.BG.set {
		parts = append(parts, fmt.Sprintf("48;2;%d;%d;%d", c.BG.r, c.BG.g, c.BG.b))
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}
