package conformance

import (
	"fmt"
	"time"

	"github.com/shellcade/kit/v2/host/gameabi"
	"github.com/shellcade/kit/v2/host/memsvc"
	"github.com/shellcade/kit/v2/host/sdk"
)

// SmokeRun configures RunShots: the deterministic inputs from a game's
// smoke.yaml. The clock contract (Epoch) and the room shape (mode, capacity,
// identities) mirror the kit smoke runner exactly — that is what makes the
// wasm path's shot bytes identical to `go run . -smoke`.
type SmokeRun struct {
	Seed   int64
	Seats  int               // seats joined before the first step (seat-0..)
	Config map[string]string // per-game config values
	Epoch  time.Time         // virtual clock start: kit smoke.SeedEpoch(Seed)
	Script Script            // Input/Wake/Advance/Shot steps (no joins)
}

// SeatFrames is one captured shot: the latest frame of each captured seat at
// the moment the Shot step ran.
type SeatFrames struct {
	Name   string
	Seats  []int
	Frames []sdk.Frame
}

// RunShots executes a smoke script against the artifact at path through the
// real adapter (limits ON) and returns the frames captured at each Shot step.
// Unlike Run it does not produce a Report: a guest fault or a shot with no
// frame is an error — smoke wants screens or a reason there are none.
func RunShots(path string, opts gameabi.Options, req SmokeRun) ([]SeatFrames, error) {
	game, err := gameabi.LoadGame(path, opts)
	if err != nil {
		return nil, fmt.Errorf("smoke: load: %w", err)
	}
	meta := game.Meta()
	if req.Seats < 1 || req.Seats > max(meta.MaxPlayers, 1) {
		return nil, fmt.Errorf("smoke: seats %d outside the game's 1..%d", req.Seats, meta.MaxPlayers)
	}

	mode := sdk.ModePrivate
	if req.Seats == 1 {
		mode = sdk.ModeSolo
	}
	cfg := sdk.RoomConfig{
		Mode:       mode,
		Capacity:   req.Seats,
		MinPlayers: min(max(meta.MinPlayers, 1), req.Seats),
		Seed:       req.Seed,
		SeedSet:    true,
	}
	factory := memsvc.NewFactory(quietLog(), nil)
	for k, v := range req.Config {
		factory.SetConfig(meta.Slug, k, []byte(v))
	}
	svc := factory.For("smoke", meta.Slug)
	h := game.NewRoom(cfg, svc)
	room := &instRoom{cfg: cfg, svc: svc, clock: req.Epoch, log: quietLog()}

	fault := func(what string) error {
		if exit, detail, faulted := gameabi.LastCallback(h); faulted {
			return fmt.Errorf("smoke: guest faulted during %s (exit %d): %s", what, exit, detail)
		}
		return nil
	}

	h.OnStart(room)
	if err := fault("start"); err != nil {
		return nil, err
	}
	for seat := 0; seat < req.Seats; seat++ {
		p := room.seatPlayer(seat)
		room.join(p)
		h.OnJoin(room, p)
		if err := fault(fmt.Sprintf("join seat %d", seat)); err != nil {
			return nil, err
		}
	}

	var shots []SeatFrames
	for i, step := range req.Script {
		switch step.Kind {
		case StepAdvance:
			room.clock = room.clock.Add(time.Duration(step.Dur) * time.Millisecond)
		case StepInput:
			h.OnInput(room, room.seatPlayer(step.Seat), inputFor(step))
		case StepWake:
			h.OnTick(room, room.clock)
		case StepShot:
			seats := step.Seats
			if seats == nil {
				seats = make([]int, req.Seats)
				for s := range seats {
					seats[s] = s
				}
			}
			shot := SeatFrames{Name: step.Name, Seats: seats}
			for _, seat := range seats {
				f, ok := room.last[room.seatPlayer(seat).AccountID]
				if !ok {
					return nil, fmt.Errorf("smoke: shot %q: seat %d has no frame yet — the game has not rendered for it", step.Name, seat)
				}
				shot.Frames = append(shot.Frames, f)
			}
			shots = append(shots, shot)
		default:
			return nil, fmt.Errorf("smoke: step %d: kind %d not allowed in a smoke script", i, step.Kind)
		}
		if err := fault(fmt.Sprintf("step %d (%s)", i, step.Note)); err != nil {
			return nil, err
		}
	}
	h.OnClose(room)
	return shots, nil
}
