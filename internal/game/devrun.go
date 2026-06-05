//go:build !wasip1 && !tinygo.wasm

package game

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
// · -handle name · -seats N. Esc or Ctrl-C leaves; Ctrl-T switches the active
// hot-seat. The input path tolerates escape sequences split across reads, paste
// bursts, and terminal resizes (SIGWINCH re-letterboxes); a terminal smaller
// than 80x24 shows a "too small" notice and resumes when grown back.
func Main(g Game) {
	seed := flag.Int64("seed", 0, "room RNG seed (0 = time-based)")
	heartbeat := flag.Duration("heartbeat", 50*time.Millisecond, "wake cadence")
	handle := flag.String("handle", "dev", "player handle (seat 1)")
	seats := flag.Int("seats", 1, "players joined to the room; Ctrl-T switches the active seat")
	cfgVals := map[string]string{}
	flag.Var(cfgFlag(cfgVals), "config", "KEY=VALUE per-game config (repeatable; value may be @file)")
	flag.Parse()

	// -seed makes the whole run reproducible: a fixed RNG seed AND a virtual
	// clock (see below). Distinguish "flag given" from "left at default 0" so a
	// deliberate -seed 0 still goes deterministic.
	seedSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "seed" {
			seedSet = true
		}
	})
	s := *seed
	if !seedSet {
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
	// Virtual clock: with -seed, the room clock starts at a fixed epoch derived
	// from the seed and advances by exactly one heartbeat per wake (and never
	// otherwise) — so a -seed run is bit-for-bit reproducible the way the wasm
	// determinism check requires. Without -seed, Now() is the wall clock.
	if seedSet {
		r.virtual = true
		r.beat = *heartbeat
		r.clock = seedEpoch(s)
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

	// Measure the terminal up front and watch for resizes (SIGWINCH). An
	// undersized terminal (<80x24) shows a "too small" notice instead of the
	// game and resumes the moment it grows back.
	r.measure(fd)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	h.OnStart(r)
	for i, p := range players {
		r.members = players[:i+1]
		h.OnJoin(r, p)
	}
	r.members = players

	// Raw stdin bytes flow on a channel; a stateful parser (devinput.go) turns
	// them into events, holding partial/ambiguous escape sequences across reads.
	raw := make(chan []byte, 8)
	readErr := make(chan struct{})
	go readRaw(raw, readErr)

	var parser keyParser
	var evbuf []parsedEvent

	// escTimer fires only while the parser holds an ambiguous bare ESC, so a
	// lone Escape resolves to "leave" without swallowing a split-read arrow.
	escTimer := time.NewTimer(escTimeout)
	if !escTimer.Stop() {
		<-escTimer.C
	}
	escArmed := false
	armEsc := func() {
		if parser.Pending() && !escArmed {
			escTimer.Reset(escTimeout)
			escArmed = true
		}
	}
	disarmEsc := func() {
		if escArmed {
			if !escTimer.Stop() {
				select {
				case <-escTimer.C:
				default:
				}
			}
			escArmed = false
		}
	}

	// dispatch applies decoded events; returns false to leave the session.
	dispatch := func(events []parsedEvent) bool {
		for _, e := range events {
			switch e.Kind {
			case evLeave:
				return false
			case evSeatSwitch:
				r.active = (r.active + 1) % len(players)
			case evInput:
				h.OnInput(r, players[r.active], e.Input)
			}
		}
		return true
	}

	tick := time.NewTicker(*heartbeat)
	defer tick.Stop()
	for !r.ended {
		select {
		case <-tick.C:
			if r.virtual {
				r.clock = r.clock.Add(r.beat) // advance once per wake, nowhere else
			}
			h.OnWake(r)
		case buf, ok := <-raw:
			if !ok {
				leaveAll(h, r, players)
				return
			}
			disarmEsc()
			evbuf = parser.Feed(buf, evbuf[:0])
			if !dispatch(evbuf) {
				leaveAll(h, r, players)
				return
			}
			armEsc()
		case <-escTimer.C:
			escArmed = false
			evbuf = parser.Timeout(evbuf[:0])
			if !dispatch(evbuf) {
				leaveAll(h, r, players)
				return
			}
		case <-winch:
			r.measure(fd)
			// Re-letterbox: clear, then repaint. A big-enough terminal repaints
			// the game via a wake; a too-small one shows the notice directly.
			fmt.Print("\x1b[2J")
			if r.tooSmall {
				r.drawTooSmall()
			} else {
				h.OnWake(r)
			}
		case <-readErr:
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

// readRaw streams raw stdin into the run loop one read at a time. A whole read
// (a single keystroke, a split escape fragment, or a multi-byte paste burst) is
// copied and sent; on EOF/error it closes readErr so the loop can leave. The
// run loop owns parsing (devinput.go), keeping this goroutine I/O-only and
// drains-without-blocking via the buffered channel.
func readRaw(out chan<- []byte, readErr chan<- struct{}) {
	buf := make([]byte, 1024) // generous: absorb paste bursts in one read
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			out <- append([]byte(nil), buf[:n]...)
		}
		if err != nil {
			close(readErr)
			return
		}
	}
}

// nativeRoom implements Room directly against the terminal + in-memory state.
type nativeRoom struct {
	cfg     RoomConfig
	members []Player
	active  int // hot-seat: which member the keyboard controls / renders
	rng     *rand.Rand
	kv      map[string][]byte // keyed by account + key
	config  map[string]string
	ended   bool

	virtual bool          // -seed: Now() reads the virtual clock below
	clock   time.Time     // virtual room clock; advances one beat per wake
	beat    time.Duration // heartbeat interval (the per-wake clock step)

	termW, termH int  // last-measured terminal size
	tooSmall     bool // true while the terminal is below 80x24
}

// measure refreshes the cached terminal size and the tooSmall flag. A failed
// query is treated as "big enough" so the game still runs in environments that
// don't report a size (the worst case is a clipped frame, not a stuck notice).
func (r *nativeRoom) measure(fd int) {
	w, h, err := term.GetSize(fd)
	if err != nil {
		r.termW, r.termH, r.tooSmall = Cols, Rows, false
		return
	}
	r.termW, r.termH = w, h
	r.tooSmall = w < Cols || h < Rows
}

// drawTooSmall paints a centered notice telling the user to grow the terminal.
// It replaces the game view entirely while undersized; the run loop repaints
// the game on the next wake once the terminal is large enough again.
func (r *nativeRoom) drawTooSmall() {
	msg := fmt.Sprintf("terminal too small %dx%d — need %dx%d", r.termW, r.termH, Cols, Rows)
	w, h := r.termW, r.termH
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	col := (w - len([]rune(msg))) / 2
	if col < 0 {
		col = 0
	}
	row := h / 2
	out := "\x1b[H\x1b[2J" + fmt.Sprintf("\x1b[%d;%dH", row+1, col+1) + msg
	os.Stdout.WriteString(out)
}

// seedEpoch derives a fixed virtual-clock start from the run seed, so the same
// -seed always begins at the same instant. The year-2000 base keeps the value
// human-readable in logs while staying well clear of the zero time.
func seedEpoch(seed int64) time.Time {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	// Spread by seed but keep it bounded and positive (mod a ~year of seconds).
	off := seed % (365 * 24 * 3600)
	if off < 0 {
		off += 365 * 24 * 3600
	}
	return base.Add(time.Duration(off) * time.Second)
}

func (r *nativeRoom) Members() []Player  { return r.members }
func (r *nativeRoom) Count() int         { return len(r.members) }
func (r *nativeRoom) Config() RoomConfig { return r.cfg }
func (r *nativeRoom) Rand() *rand.Rand   { return r.rng }
func (r *nativeRoom) Now() time.Time {
	if r.virtual {
		return r.clock
	}
	return time.Now()
}
func (r *nativeRoom) Settled() bool { return r.ended }

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
	if r.tooSmall {
		r.drawTooSmall() // hold the notice; the game repaints once resized
		return
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
			// Burst the grapheme cluster's extra code points immediately after
			// the base, before the next cell, so the terminal receives the
			// cluster (base VS16 / base + keycap / base ZWJ piece) unbroken.
			if c.Cp2 != 0 {
				b.WriteRune(c.Cp2)
				if c.Cp3 != 0 {
					b.WriteRune(c.Cp3)
				}
			}
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
