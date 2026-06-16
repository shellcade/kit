package sdk

import (
	"fmt"
	"sync"
	"testing"
)

type stubGame struct {
	GameBase
	meta GameMeta
}

func (g *stubGame) Meta() GameMeta                               { return g.meta }
func (g *stubGame) NewRoom(cfg RoomConfig, svc Services) Handler { return Base{} }

func stub(slug string) Game {
	return &stubGame{meta: GameMeta{Slug: slug, Name: slug, MinPlayers: 1, MaxPlayers: 2}}
}

func hiddenStub(slug string) Game {
	return &stubGame{meta: GameMeta{Slug: slug, Name: slug, MinPlayers: 1, MaxPlayers: 2, Hidden: true}}
}

// Listed returns only non-hidden games (the lobby's player-facing menu), while
// All / Get still include hidden games so quick-match-by-slug and admin reach
// them (add-loadtest-harness).
func TestRegistryListedExcludesHidden(t *testing.T) {
	r := NewRegistry()
	r.MustAdd(stub("a"))
	r.MustAdd(hiddenStub("shellcade/loadtest"))
	r.MustAdd(stub("b"))

	var listed []string
	for _, g := range r.Listed() {
		listed = append(listed, g.Meta().Slug)
	}
	if len(listed) != 2 || listed[0] != "a" || listed[1] != "b" {
		t.Fatalf("Listed() = %v, want [a b] (hidden excluded)", listed)
	}
	if len(r.All()) != 3 {
		t.Fatalf("All() = %d games, want 3 (hidden included)", len(r.All()))
	}
	if _, ok := r.Get("shellcade/loadtest"); !ok {
		t.Fatal("hidden game must still be resolvable by slug")
	}
}

func TestRegistryAddRemove(t *testing.T) {
	r := NewRegistry()
	r.MustAdd(stub("a"))
	r.MustAdd(stub("b"))
	r.MustAdd(stub("c"))

	if err := r.Add(stub("b")); err == nil {
		t.Fatal("duplicate slug accepted")
	}
	g, ok := r.Remove("b")
	if !ok || g.Meta().Slug != "b" {
		t.Fatalf("Remove(b) = %v %v", g, ok)
	}
	if _, ok := r.Remove("b"); ok {
		t.Fatal("double remove succeeded")
	}
	if _, ok := r.Get("b"); ok {
		t.Fatal("removed game still gettable")
	}
	var slugs []string
	for _, g := range r.All() {
		slugs = append(slugs, g.Meta().Slug)
	}
	if len(slugs) != 2 || slugs[0] != "a" || slugs[1] != "c" {
		t.Fatalf("order after remove = %v, want [a c]", slugs)
	}
	// Re-adding appends.
	r.MustAdd(stub("b"))
	if all := r.All(); all[len(all)-1].Meta().Slug != "b" {
		t.Fatal("re-added game not appended")
	}
}

// TestRegistryConcurrentAccess exercises live reads during add/remove churn —
// meaningful under -race (task 4.3).
func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 8; i++ {
		r.MustAdd(stub(fmt.Sprintf("seed-%d", i)))
	}
	var readers, writers sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ { // readers: the lobby view
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, g := range r.All() {
					_ = g.Meta().Slug
				}
				_, _ = r.Get("seed-3")
			}
		}()
	}
	for w := 0; w < 2; w++ { // writers: catalog add/remove churn
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; i < 500; i++ {
				slug := fmt.Sprintf("churn-%d-%d", w, i)
				_ = r.Add(stub(slug))
				_, _ = r.Remove(slug)
			}
		}(w)
	}
	writers.Add(1)
	go func() { // quarantine-style remove/re-add of a seed game
		defer writers.Done()
		for i := 0; i < 500; i++ {
			if g, ok := r.Remove("seed-7"); ok {
				_ = r.Add(g)
			}
		}
	}()
	writers.Wait()
	close(stop)
	readers.Wait()
}
