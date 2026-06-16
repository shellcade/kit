package sdk

import (
	"fmt"
	"sync"
)

// Registry is the game roster the hub is constructed with. It is a real value so
// tests and serve --dev can build a curated roster without global mutation.
// It is safe for concurrent use: the lobby reads the LIVE view while dynamic
// games (wasm catalog, quarantine) add and remove entries at runtime.
type Registry struct {
	mu    sync.RWMutex
	games map[string]Game
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{games: map[string]Game{}}
}

// Add registers a game. A duplicate slug is an error.
func (r *Registry) Add(g Game) error {
	slug := g.Meta().Slug
	if slug == "" {
		return fmt.Errorf("game has empty slug")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.games[slug]; dup {
		return fmt.Errorf("duplicate game slug %q", slug)
	}
	r.games[slug] = g
	r.order = append(r.order, slug)
	return nil
}

// MustAdd registers a game, panicking on a duplicate slug (fatal).
func (r *Registry) MustAdd(g Game) {
	if err := r.Add(g); err != nil {
		panic(err)
	}
}

// Remove unregisters a game by slug, returning it (so a quarantine or admin
// flow can restore it later). Rooms already running the game are untouched —
// they hold their own Game reference; removal only stops NEW rooms and lobby
// listing. Re-adding preserves nothing of the old position: it appends.
func (r *Registry) Remove(slug string) (Game, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.games[slug]
	if !ok {
		return nil, false
	}
	delete(r.games, slug)
	for i, s := range r.order {
		if s == slug {
			r.order = append(r.order[:i:i], r.order[i+1:]...)
			break
		}
	}
	return g, true
}

// Get looks up a game by slug.
func (r *Registry) Get(slug string) (Game, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.games[slug]
	return g, ok
}

// All returns the games in stable registration order, INCLUDING hidden ones —
// the set quick-match-by-slug, direct entry, and admin reach.
func (r *Registry) All() []Game {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Game, 0, len(r.order))
	for _, slug := range r.order {
		out = append(out, r.games[slug])
	}
	return out
}

// Listed returns the games for the lobby's player-facing menu in stable
// registration order, EXCLUDING any with Meta().Hidden set (add-loadtest-harness).
func (r *Registry) Listed() []Game {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Game, 0, len(r.order))
	for _, slug := range r.order {
		if g := r.games[slug]; !g.Meta().Hidden {
			out = append(out, g)
		}
	}
	return out
}

// defaultRegistry backs sdk.Register so games can register in init().
var defaultRegistry = NewRegistry()

// Register writes a game into the default registry. A duplicate slug is fatal.
func Register(g Game) { defaultRegistry.MustAdd(g) }

// Default returns the package-level default registry.
func Default() *Registry { return defaultRegistry }
