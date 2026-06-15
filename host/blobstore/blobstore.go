// Package blobstore is the arcade's wasm blob storage: published catalog
// artifacts (`artifacts/<sha256>.wasm`, immutable, content-addressed) and
// room state under the `snapshots/` prefix, in two coexisting schemes:
//   - flat hibernation snapshots (`snapshots/<room-id>`, deleted on restore) —
//     the original disposing park/resume path (internal/gameabi);
//   - versioned, MAC'd room checkpoints (`snapshots/<room-id>/<epoch>` with a
//     `snapshots/<room-id>/latest` pointer; see CheckpointKey / Sealer) — the
//     non-destructive periodic-durability path added for regional bastions.
//
// Both live under `snapshots/` and share the bucket lifecycle TTL on that
// prefix; Phase 0 keeps them side by side and Track C / task G.5 unifies room
// durability onto the versioned scheme. Production is a Fly-attached Tigris
// bucket via the standard S3 env contract; dev and tests use the in-memory
// double. Only the arcade ever holds bucket credentials — catalog CI
// publishes to GitHub releases and never writes here.
package blobstore

import (
	"context"
	"sort"
	"sync"
)

// Store is the blob surface the catalog pipeline and the hibernation store
// build on. Keys are slash-separated paths; values are whole blobs (wasm
// artifacts and zstd snapshots are small enough that streaming buys nothing
// on a 32 MiB-capped guest).
type Store interface {
	// Get returns the blob at key; ok=false when it does not exist.
	Get(ctx context.Context, key string) (data []byte, ok bool, err error)
	// Put writes the blob at key, overwriting any existing object.
	Put(ctx context.Context, key string, data []byte) error
	// Delete removes the blob at key; deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns the keys under prefix in lexical order.
	List(ctx context.Context, prefix string) ([]string, error)
}

// Memory is the in-memory Store double for dev mode and tests.
type Memory struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{blobs: map[string][]byte{}}
}

func (m *Memory) Get(ctx context.Context, key string) ([]byte, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.blobs[key]
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, true, nil
}

func (m *Memory) Put(ctx context.Context, key string, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = cp
	return nil
}

func (m *Memory) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, key)
	return nil
}

func (m *Memory) List(ctx context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.blobs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
