package smoke

import (
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/shellcade/kit/v2/internal/game"
)

// keyNames maps `key:` step values to named keys. `space` is sugar for the
// rune ' ' (space is printable, not a named key in the SDK).
var keyNames = map[string]game.Key{
	"enter":     game.KeyEnter,
	"backspace": game.KeyBackspace,
	"esc":       game.KeyEsc,
	"escape":    game.KeyEsc,
	"tab":       game.KeyTab,
	"up":        game.KeyUp,
	"down":      game.KeyDown,
	"left":      game.KeyLeft,
	"right":     game.KeyRight,
}

// shotName is the safe-filename vocabulary for shot names.
var shotName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Parse decodes and validates a smoke.yaml. Errors name the offending line.
// `text:` steps are expanded to one rune step per character at parse time, so
// the executor sees only the six primitive kinds.
func Parse(b []byte) (*Script, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("smoke.yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("smoke.yaml: top level must be a mapping")
	}
	root := doc.Content[0]

	s := &Script{Heartbeat: DefaultHeartbeat}
	var seenSeed, seenSeats, seenSteps bool
	var stepsNode *yaml.Node

	for i := 0; i < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch k.Value {
		case "seed":
			if err := v.Decode(&s.Seed); err != nil {
				return nil, lineErr(v, "seed must be an integer")
			}
			seenSeed = true
		case "seats":
			if err := v.Decode(&s.Seats); err != nil || s.Seats < 1 || s.Seats > 8 {
				return nil, lineErr(v, "seats must be an integer in 1..8")
			}
			seenSeats = true
		case "heartbeat":
			d, err := time.ParseDuration(v.Value)
			if err != nil || d <= 0 {
				return nil, lineErr(v, "heartbeat must be a positive duration (e.g. 50ms)")
			}
			s.Heartbeat = d
		case "config":
			if v.Kind != yaml.MappingNode {
				return nil, lineErr(v, "config must be a mapping of string keys to scalar values")
			}
			s.Config = map[string]string{}
			for j := 0; j < len(v.Content); j += 2 {
				ck, cv := v.Content[j], v.Content[j+1]
				if cv.Kind != yaml.ScalarNode {
					return nil, lineErr(cv, "config %q: value must be a scalar", ck.Value)
				}
				s.Config[ck.Value] = cv.Value
			}
		case "steps":
			stepsNode = v
			seenSteps = true
		default:
			return nil, lineErr(k, "unknown field %q (want seed, seats, heartbeat, config, steps)", k.Value)
		}
	}
	if !seenSeed {
		return nil, fmt.Errorf("smoke.yaml: missing required field seed")
	}
	if !seenSeats {
		return nil, fmt.Errorf("smoke.yaml: missing required field seats")
	}
	if !seenSteps {
		return nil, fmt.Errorf("smoke.yaml: missing required field steps")
	}
	if stepsNode.Kind != yaml.SequenceNode || len(stepsNode.Content) == 0 {
		return nil, lineErr(stepsNode, "steps must be a non-empty list")
	}

	shots := map[string]bool{}
	for _, n := range stepsNode.Content {
		steps, err := parseStep(n, s, shots)
		if err != nil {
			return nil, err
		}
		s.Steps = append(s.Steps, steps...)
	}
	if len(shots) == 0 {
		return nil, fmt.Errorf("smoke.yaml: script has no shot step — nothing would be captured")
	}
	return s, nil
}

// parseStep decodes one step node. A step is a mapping with exactly one step
// key — except shot, which allows a sibling `seats:` filter.
func parseStep(n *yaml.Node, s *Script, shots map[string]bool) ([]Step, error) {
	if n.Kind != yaml.MappingNode || len(n.Content) < 2 {
		return nil, lineErr(n, "each step must be a mapping like `- key: enter`")
	}
	k, v := n.Content[0], n.Content[1]
	st := Step{Line: k.Line}

	extra := n.Content[2:]
	if k.Value != "shot" && len(extra) > 0 {
		return nil, lineErr(n.Content[2], "unexpected extra field %q in %s step", n.Content[2].Value, k.Value)
	}

	switch k.Value {
	case "rune":
		r, size := utf8.DecodeRuneInString(v.Value)
		if r == utf8.RuneError || size != len(v.Value) {
			return nil, lineErr(v, "rune must be exactly one printable character, got %q", v.Value)
		}
		st.Kind, st.Rune = StepRune, r
	case "key":
		if key, ok := keyNames[v.Value]; ok {
			st.Kind, st.Key = StepKey, key
		} else if v.Value == "space" {
			st.Kind, st.Rune = StepRune, ' '
		} else {
			return nil, lineErr(v, "unknown key %q (want enter, backspace, esc, tab, up, down, left, right, space)", v.Value)
		}
	case "text":
		if v.Value == "" {
			return nil, lineErr(v, "text must be a non-empty string")
		}
		var steps []Step
		for _, r := range v.Value {
			steps = append(steps, Step{Kind: StepRune, Line: k.Line, Rune: r})
		}
		return steps, nil
	case "seat":
		var seat int
		if err := v.Decode(&seat); err != nil || seat < 0 || seat >= s.Seats {
			return nil, lineErr(v, "seat must be an integer in 0..%d", s.Seats-1)
		}
		st.Kind, st.Seat = StepSeat, seat
	case "advance":
		d, err := time.ParseDuration(v.Value)
		if err != nil || d <= 0 {
			return nil, lineErr(v, "advance must be a positive duration (e.g. 1.5s)")
		}
		if d%s.Heartbeat != 0 {
			return nil, lineErr(v, "advance %s is not a multiple of the %s heartbeat — pick an exact tick", d, s.Heartbeat)
		}
		st.Kind, st.D = StepAdvance, d
	case "wake":
		if v.Kind == yaml.ScalarNode && v.Value != "" && v.Tag != "!!null" {
			return nil, lineErr(v, "wake takes no value")
		}
		st.Kind = StepWake
	case "shot":
		if !shotName.MatchString(v.Value) {
			return nil, lineErr(v, "shot name %q must match %s (it becomes a filename)", v.Value, shotName)
		}
		if shots[v.Value] {
			return nil, lineErr(v, "duplicate shot name %q", v.Value)
		}
		shots[v.Value] = true
		st.Kind, st.Name = StepShot, v.Value
		if len(extra) > 0 {
			if extra[0].Value != "seats" || len(extra) != 2 {
				return nil, lineErr(extra[0], "shot allows only a `seats:` filter alongside the name")
			}
			fv := extra[1]
			if fv.Kind != yaml.SequenceNode || len(fv.Content) == 0 {
				return nil, lineErr(fv, "shot seats must be a non-empty list of seat indices")
			}
			seen := map[int]bool{}
			for _, e := range fv.Content {
				var seat int
				if err := e.Decode(&seat); err != nil || seat < 0 || seat >= s.Seats {
					return nil, lineErr(e, "shot seat must be an integer in 0..%d", s.Seats-1)
				}
				if seen[seat] {
					return nil, lineErr(e, "duplicate seat %d in shot filter", seat)
				}
				seen[seat] = true
				st.Seats = append(st.Seats, seat)
			}
		}
	default:
		return nil, lineErr(k, "unknown step %q (want rune, key, text, seat, advance, wake, shot)", k.Value)
	}
	return []Step{st}, nil
}

func lineErr(n *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("smoke.yaml:%d: %s", n.Line, fmt.Sprintf(format, args...))
}
