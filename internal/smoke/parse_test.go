package smoke

import (
	"strings"
	"testing"
	"time"

	"github.com/shellcade/kit/v2/internal/game"
)

const minimal = `
seed: 42
seats: 2
steps:
  - shot: lobby
`

func TestParseMinimal(t *testing.T) {
	s, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if s.Seed != 42 || s.Seats != 2 || s.Heartbeat != DefaultHeartbeat {
		t.Fatalf("got %+v", s)
	}
	if len(s.Steps) != 1 || s.Steps[0].Kind != StepShot || s.Steps[0].Name != "lobby" {
		t.Fatalf("steps: %+v", s.Steps)
	}
}

func TestParseEveryStepKind(t *testing.T) {
	src := `
seed: 7
seats: 3
heartbeat: 100ms
config:
  variant: classic
  level: 3
steps:
  - shot: start
  - rune: "5"
  - key: enter
  - key: space
  - text: "ab"
  - seat: 2
  - advance: 1s
  - wake:
  - shot: end
    seats: [2, 0]
`
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s.Heartbeat != 100*time.Millisecond {
		t.Fatalf("heartbeat: %v", s.Heartbeat)
	}
	if s.Config["variant"] != "classic" || s.Config["level"] != "3" {
		t.Fatalf("config: %+v", s.Config)
	}
	want := []StepKind{StepShot, StepRune, StepKey, StepRune, StepRune, StepRune, StepSeat, StepAdvance, StepWake, StepShot}
	if len(s.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(s.Steps), len(want), s.Steps)
	}
	for i, k := range want {
		if s.Steps[i].Kind != k {
			t.Fatalf("step %d: kind %v, want %v", i, s.Steps[i].Kind, k)
		}
	}
	// key: space becomes a rune step; text expands per character.
	if s.Steps[3].Rune != ' ' || s.Steps[4].Rune != 'a' || s.Steps[5].Rune != 'b' {
		t.Fatalf("rune expansion: %+v", s.Steps[3:6])
	}
	if s.Steps[2].Key != game.KeyEnter {
		t.Fatalf("key: %v", s.Steps[2].Key)
	}
	if got := s.Steps[9].Seats; len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Fatalf("shot filter: %v", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, src, wantErr string
	}{
		{"missing seed", "seats: 1\nsteps: [{shot: a}]", "missing required field seed"},
		{"missing seats", "seed: 1\nsteps: [{shot: a}]", "missing required field seats"},
		{"missing steps", "seed: 1\nseats: 1", "missing required field steps"},
		{"seats range", "seed: 1\nseats: 9\nsteps: [{shot: a}]", "seats must be"},
		{"unknown field", "seed: 1\nseats: 1\nfoo: 1\nsteps: [{shot: a}]", `unknown field "foo"`},
		{"unknown step", "seed: 1\nseats: 1\nsteps: [{zap: 1}]", `unknown step "zap"`},
		{"unknown key", "seed: 1\nseats: 1\nsteps: [{key: bogus}, {shot: a}]", `unknown key "bogus"`},
		{"multi-rune", `{seed: 1, seats: 1, steps: [{rune: "ab"}, {shot: a}]}`, "exactly one printable character"},
		{"seat range", "seed: 1\nseats: 2\nsteps: [{seat: 2}, {shot: a}]", "seat must be an integer in 0..1"},
		{"advance not tick", "seed: 1\nseats: 1\nsteps: [{advance: 75ms}, {shot: a}]", "not a multiple"},
		{"advance bad", "seed: 1\nseats: 1\nsteps: [{advance: nope}, {shot: a}]", "advance must be"},
		{"dup shot", "seed: 1\nseats: 1\nsteps: [{shot: a}, {shot: a}]", `duplicate shot name "a"`},
		{"shot name", "seed: 1\nseats: 1\nsteps: [{shot: 'a/b'}]", "must match"},
		{"no shots", "seed: 1\nseats: 1\nsteps: [{wake: }]", "no shot step"},
		{"shot filter range", "seed: 1\nseats: 2\nsteps: [{shot: a, seats: [3]}]", "shot seat must be"},
		{"shot filter dup", "seed: 1\nseats: 2\nsteps: [{shot: a, seats: [0, 0]}]", "duplicate seat 0"},
		{"shot extra field", "seed: 1\nseats: 1\nsteps: [{shot: a, frames: 2}]", "only a `seats:` filter"},
		{"step extra field", "seed: 1\nseats: 1\nsteps: [{wake: , bogus: 1}, {shot: a}]", "unexpected extra field"},
		{"empty text", `{seed: 1, seats: 1, steps: [{text: ""}, {shot: a}]}`, "non-empty"},
		{"heartbeat bad", "seed: 1\nseats: 1\nheartbeat: -5ms\nsteps: [{shot: a}]", "heartbeat must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseErrorsCarryLineNumbers(t *testing.T) {
	src := "seed: 1\nseats: 1\nsteps:\n  - shot: ok\n  - key: bogus\n"
	_, err := Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "smoke.yaml:5:") {
		t.Fatalf("want line 5 in error, got: %v", err)
	}
}
