package kittest_test

// KVUnavailable mirrors the production host's KV degradation byte-for-byte:
// the ABI has no error channel, so a failing store never surfaces a Go error —
// Get reports the key as missing and Set/Delete silently drop the write. These
// tests pin those exact semantics and demonstrate the hazard the knob exists
// to expose: the natural `Get → missing → initialize → Set` wallet pattern
// resets a player's saved balance during a store blip.

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	kit "github.com/shellcade/kit/v2"
	"github.com/shellcade/kit/v2/kittest"
)

func TestKVUnavailableDegradationSemantics(t *testing.T) {
	ctx := context.Background()
	r := kittest.NewRoom(kittest.Player("p1"))
	store := r.Services().Accounts.For(r.Players[0]).Store()

	// Persist a wallet while the store is healthy.
	if err := store.Set(ctx, "balance", []byte("990"), kit.MergeSum); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// During the outage: Get is (nil, false, nil) — missing, NOT an error.
	r.KVUnavailable = true
	v, ok, err := store.Get(ctx, "balance")
	if v != nil || ok || err != nil {
		t.Fatalf("unavailable Get = (%v, %v, %v), want (nil, false, nil)", v, ok, err)
	}

	// Set returns nil but persists nothing; the durable value is untouched.
	if err := store.Set(ctx, "balance", []byte("1000"), kit.MergeSum); err != nil {
		t.Fatalf("unavailable Set must return nil, got %v", err)
	}
	if got := string(r.KV["p1"]["balance"]); got != "990" {
		t.Fatalf("unavailable Set persisted: balance = %q, want %q", got, "990")
	}

	// Delete returns nil but drops nothing.
	if err := store.Delete(ctx, "balance"); err != nil {
		t.Fatalf("unavailable Delete must return nil, got %v", err)
	}
	if _, still := r.KV["p1"]["balance"]; !still {
		t.Fatal("unavailable Delete removed the key")
	}

	// The blip ends: the durable value reads back intact.
	r.KVUnavailable = false
	v, ok, err = store.Get(ctx, "balance")
	if err != nil || !ok || string(v) != "990" {
		t.Fatalf("post-outage Get = (%q, %v, %v), want (\"990\", true, nil)", v, ok, err)
	}
}

// ExampleRoom_kvUnavailable shows the read-absent-reinit hazard: a wallet
// loader that treats "missing" as "new player" silently resets saved state
// during a store blip — kittest can now make that test fail before production
// does.
func ExampleRoom_kvUnavailable() {
	ctx := context.Background()
	r := kittest.NewRoom(kittest.Player("p1"))
	store := r.Services().Accounts.For(r.Players[0]).Store()

	// The naive pattern: Get → missing → initialize starting balance → Set.
	loadWallet := func() int {
		v, ok, _ := store.Get(ctx, "balance")
		if !ok {
			store.Set(ctx, "balance", []byte("1000"), kit.MergeMax)
			return 1000
		}
		n, _ := strconv.Atoi(string(v))
		return n
	}

	store.Set(ctx, "balance", []byte("9500"), kit.MergeMax) // a veteran's wallet
	fmt.Println("healthy:", loadWallet())

	r.KVUnavailable = true // a transient store blip…
	fmt.Println("blip:   ", loadWallet())

	r.KVUnavailable = false // …but here the reinit Set was dropped too, so the
	fmt.Println("after:  ", loadWallet()) // durable 9500 survives. In production
	// the blip can end between the Get and the Set — and then the 1000
	// overwrites 9500. Don't persist a starting balance from a missing read.

	// Output:
	// healthy: 9500
	// blip:    1000
	// after:   9500
}
