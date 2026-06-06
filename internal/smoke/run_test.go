package smoke

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/internal/game"
)

func parse(t *testing.T, src string) *Script {
	t.Helper()
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunAdvanceSweepsWakes(t *testing.T) {
	s := parse(t, `
seed: 1
seats: 1
heartbeat: 50ms
steps:
  - advance: 1.5s
  - shot: after
`)
	shots, err := Run(fixture{}, s)
	if err != nil {
		t.Fatal(err)
	}
	text := string(RenderText(shots[0].Frames[0]))
	if !strings.Contains(text, "wakes=30") {
		t.Fatalf("1.5s @ 50ms should wake 30 times, got: %s", text)
	}
	// The clock moved exactly 1.5s past the seed epoch.
	wantClock := game.SeedEpoch(1).Add(1500 * 1e6).Unix()
	if !strings.Contains(text, "clock=") || !strings.Contains(text, fmtInt(wantClock)) {
		t.Fatalf("clock: got %s want unix %d", text, wantClock)
	}
}

func fmtInt(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRunSeatRoutingAndPerSeatShots(t *testing.T) {
	s := parse(t, `
seed: 1
seats: 2
steps:
  - rune: "a"
  - seat: 1
  - rune: "b"
  - key: enter
  - shot: state
`)
	shots, err := Run(fixture{}, s)
	if err != nil {
		t.Fatal(err)
	}
	shot := shots[0]
	if shot.Collapsed() {
		t.Fatal("per-seat fixture frames must not collapse")
	}
	t0 := string(RenderText(shot.Frames[0]))
	t1 := string(RenderText(shot.Frames[1]))
	if !strings.Contains(t0, "in=a") || strings.Contains(t0, "in=ab") {
		t.Fatalf("seat0 inputs: %s", t0)
	}
	if !strings.Contains(t1, "in=b<1>") {
		t.Fatalf("seat1 inputs (rune b + enter): %s", t1)
	}
	if !strings.Contains(t0, "seat=seat-0") || !strings.Contains(t1, "seat=seat-1") {
		t.Fatalf("identities: %s / %s", t0, t1)
	}
}

func TestRunBroadcastCollapses(t *testing.T) {
	s := parse(t, `
seed: 1
seats: 3
steps:
  - shot: board
`)
	shots, err := Run(fixture{broadcast: true}, s)
	if err != nil {
		t.Fatal(err)
	}
	if !shots[0].Collapsed() {
		t.Fatal("identical broadcast frames must collapse")
	}
	if len(shots[0].Frames) != 3 {
		t.Fatalf("still captures all seats: %d", len(shots[0].Frames))
	}
}

func TestRunShotSeatFilterSorted(t *testing.T) {
	s := parse(t, `
seed: 1
seats: 3
steps:
  - shot: two
    seats: [2, 0]
`)
	shots, err := Run(fixture{}, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := shots[0].Seats; !sort.IntsAreSorted(got) || len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("filter seats: %v", got)
	}
}

func TestRunDeterministic(t *testing.T) {
	s := parse(t, `
seed: 99
seats: 2
steps:
  - rune: "x"
  - advance: 250ms
  - shot: a
  - seat: 1
  - text: "yz"
  - wake:
  - shot: b
`)
	run := func() [][]byte {
		shots, err := Run(fixture{}, s)
		if err != nil {
			t.Fatal(err)
		}
		var out [][]byte
		for _, sh := range shots {
			for _, f := range sh.Frames {
				out = append(out, RenderANSI(f))
			}
		}
		return out
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatal("shot count differs across runs")
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("run not deterministic at frame %d", i)
		}
	}
}

func TestRunSeedChangesDraw(t *testing.T) {
	one := parse(t, "seed: 1\nseats: 1\nsteps: [{shot: a}]")
	two := parse(t, "seed: 2\nseats: 1\nsteps: [{shot: a}]")
	s1, err := Run(fixture{}, one)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Run(fixture{}, two)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(RenderText(s1[0].Frames[0]), RenderText(s2[0].Frames[0])) {
		t.Fatal("different seeds should change the RNG draw")
	}
}

func TestRunSeatsExceedMaxPlayers(t *testing.T) {
	s := parse(t, "seed: 1\nseats: 5\nsteps: [{shot: a}]")
	if _, err := Run(fixture{}, s); err == nil || !strings.Contains(err.Error(), "maxPlayers") {
		t.Fatalf("want maxPlayers error, got %v", err)
	}
}

func TestRunShotBeforeAnyFrame(t *testing.T) {
	s := parse(t, "seed: 1\nseats: 1\nsteps: [{shot: a}]")
	if _, err := Run(silent{}, s); err == nil || !strings.Contains(err.Error(), "no frame yet") {
		t.Fatalf("want no-frame error, got %v", err)
	}
}

// silent renders nothing — the shot-before-frame error case.
type silent struct{}

func (silent) Meta() game.GameMeta {
	return game.GameMeta{Slug: "silent", Name: "Silent", MinPlayers: 1, MaxPlayers: 4}
}
func (silent) NewRoom(game.RoomConfig, game.Services) game.Handler { return &silentRoom{} }

type silentRoom struct{ game.Base }

func TestWriteShotsNaming(t *testing.T) {
	dir := t.TempDir()
	s := parse(t, `
seed: 1
seats: 2
steps:
  - shot: lobby
  - rune: "q"
  - shot: after
`)
	shots, err := Run(fixture{broadcast: true}, s)
	if err != nil {
		t.Fatal(err)
	}
	// Make the second shot non-collapsing by re-running per-seat fixture.
	shots2, err := Run(fixture{}, s)
	if err != nil {
		t.Fatal(err)
	}
	names, err := WriteShots(dir, []Shot{shots[0], shots2[1]})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"01-lobby.ansi", "01-lobby.txt",
		"02-after.seat0.ansi", "02-after.seat0.txt",
		"02-after.seat1.ansi", "02-after.seat1.txt",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names: %v, want %v", names, want)
	}
	for _, n := range want {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("missing %s", n)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "01-lobby.ansi"))
	if c := bytes.Count(b, []byte("\n")); c != game.Rows {
		t.Fatalf("ansi line count: %d, want %d", c, game.Rows)
	}
}
