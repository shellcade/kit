package blobstore

import (
	"bytes"
	"errors"
	"sort"
	"testing"
)

// CheckpointKey formats snapshots/<roomID>/<epoch> with a 20-digit zero-padded
// epoch, and LatestPointerKey points at the per-room latest pointer.
func TestCheckpointKeyFormat(t *testing.T) {
	const room = "0190b8a0-1234-7abc-8def-0123456789ab"
	if got, want := CheckpointKey(room, 0), "snapshots/"+room+"/00000000000000000000"; got != want {
		t.Errorf("CheckpointKey(%q, 0) = %q, want %q", room, got, want)
	}
	if got, want := CheckpointKey(room, 42), "snapshots/"+room+"/00000000000000000042"; got != want {
		t.Errorf("CheckpointKey(%q, 42) = %q, want %q", room, got, want)
	}
	if got, want := LatestPointerKey(room), "snapshots/"+room+"/latest"; got != want {
		t.Errorf("LatestPointerKey(%q) = %q, want %q", room, got, want)
	}
}

// A negative epoch is a contract violation (the '-' sign would break lexical
// ordering): CheckpointKey panics rather than mint a misordering key.
func TestCheckpointKeyNegativeEpochPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("CheckpointKey(room, -1) did not panic")
		}
	}()
	_ = CheckpointKey("room-uuid", -1)
}

// Zero-padding makes lexical key order match numeric epoch order, so List under
// the room prefix returns checkpoints oldest-to-newest.
func TestCheckpointKeyLexicalOrder(t *testing.T) {
	const room = "room-uuid"
	epochs := []int64{0, 1, 2, 9, 10, 99, 100, 1000, 999999999}
	keys := make([]string, len(epochs))
	for i, e := range epochs {
		keys[i] = CheckpointKey(room, e)
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for i := range keys {
		if keys[i] != sorted[i] {
			t.Fatalf("lexical order != numeric order at %d: %q vs %q", i, keys[i], sorted[i])
		}
	}
}

// Seal/Open round-trips: an opened sealed blob equals the original payload.
func TestSealerRoundTrip(t *testing.T) {
	s := NewHMACSealer([]byte("server-side-key"))
	payload := []byte("room checkpoint bytes")
	sealed := s.Seal(payload)
	if bytes.Equal(sealed, payload) {
		t.Fatal("Seal returned the payload unchanged (no MAC appended)")
	}
	got, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("Open = %q, want %q", got, payload)
	}
}

// A tampered sealed blob fails Open with ErrSealVerify before any payload is
// returned (spec: verified before restore writes guest memory).
func TestSealerTamperRejected(t *testing.T) {
	s := NewHMACSealer([]byte("server-side-key"))
	sealed := s.Seal([]byte("checkpoint"))

	flip := append([]byte(nil), sealed...)
	flip[0] ^= 0xff // corrupt the payload region
	if _, err := s.Open(flip); !errors.Is(err, ErrSealVerify) {
		t.Fatalf("Open(tampered payload) err = %v, want ErrSealVerify", err)
	}

	flipMac := append([]byte(nil), sealed...)
	flipMac[len(flipMac)-1] ^= 0xff // corrupt the MAC region
	if _, err := s.Open(flipMac); !errors.Is(err, ErrSealVerify) {
		t.Fatalf("Open(tampered mac) err = %v, want ErrSealVerify", err)
	}

	if _, err := s.Open([]byte("short")); !errors.Is(err, ErrSealVerify) {
		t.Fatalf("Open(too short) err = %v, want ErrSealVerify", err)
	}
}

// A blob sealed under one key does not Open under another — artifact-SHA
// equality is NOT blob integrity; the server-side key is.
func TestSealerWrongKeyRejected(t *testing.T) {
	sealed := NewHMACSealer([]byte("key-a")).Seal([]byte("checkpoint"))
	if _, err := NewHMACSealer([]byte("key-b")).Open(sealed); !errors.Is(err, ErrSealVerify) {
		t.Fatalf("Open under wrong key err = %v, want ErrSealVerify", err)
	}
}
