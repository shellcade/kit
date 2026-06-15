package gameabi

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

// Quarantine is the fault-count watchdog: wire its RecordFault into
// Options.OnFault and a game that faults Threshold times within Window is
// removed from the live roster (new rooms and lobby listing stop; running
// rooms are spared — they hold their own Game reference). Removal is
// admin-reversible via Restore. Every transition is audit-logged.
type Quarantine struct {
	reg       *sdk.Registry
	threshold int
	window    time.Duration
	log       *slog.Logger
	now       func() time.Time // injectable for tests

	// OnQuarantine, when set, is told the slug of a game the watchdog has just
	// pulled from the live roster, so the catalog can flip its metadata state to
	// quarantined. Called under the watchdog lock from a room-actor goroutine;
	// keep it quick and non-blocking. Optional (nil for non-catalog callers, e.g.
	// dev sideloads).
	OnQuarantine func(slug string)

	mu      sync.Mutex
	faults  map[string][]time.Time
	removed map[string]sdk.Game
}

// NewQuarantine builds a watchdog over reg. threshold <= 0 defaults to 3
// faults; window <= 0 defaults to 10 minutes.
func NewQuarantine(reg *sdk.Registry, threshold int, window time.Duration, log *slog.Logger) *Quarantine {
	if threshold <= 0 {
		threshold = 3
	}
	if window <= 0 {
		window = 10 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Quarantine{
		reg:       reg,
		threshold: threshold,
		window:    window,
		log:       log,
		now:       time.Now,
		faults:    map[string][]time.Time{},
		removed:   map[string]sdk.Game{},
	}
}

// RecordFault counts one guest fault for slug and quarantines the game when
// the in-window count reaches the threshold. Safe from any goroutine (room
// actors report concurrently).
func (q *Quarantine) RecordFault(slug string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	keep := q.faults[slug][:0]
	for _, at := range q.faults[slug] {
		if now.Sub(at) < q.window {
			keep = append(keep, at)
		}
	}
	keep = append(keep, now)
	q.faults[slug] = keep
	if len(keep) < q.threshold {
		return
	}
	if _, already := q.removed[slug]; already {
		return // faults from rooms still running a quarantined game
	}
	g, ok := q.reg.Remove(slug)
	if !ok {
		return // not in the live roster (sideload removed, never added)
	}
	q.removed[slug] = g
	delete(q.faults, slug)
	q.log.Warn("audit: game quarantined",
		"event", "game.quarantine",
		"slug", slug,
		"faults", len(keep),
		"window", q.window.String(),
	)
	if q.OnQuarantine != nil {
		q.OnQuarantine(slug)
	}
}

// Quarantined returns the slugs currently held out of the roster.
func (q *Quarantine) Quarantined() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, 0, len(q.removed))
	for slug := range q.removed {
		out = append(out, slug)
	}
	return out
}

// Restore returns a quarantined game to the live roster with a clean fault
// count (the admin-reversible half of the contract).
func (q *Quarantine) Restore(slug string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	g, ok := q.removed[slug]
	if !ok {
		return fmt.Errorf("gameabi: %q is not quarantined", slug)
	}
	if err := q.reg.Add(g); err != nil {
		return err
	}
	delete(q.removed, slug)
	q.log.Warn("audit: game restored from quarantine",
		"event", "game.quarantine.restore",
		"slug", slug,
	)
	return nil
}
