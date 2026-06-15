package blobstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
)

// This file is the room-checkpoint contract (design D5, room-hosting spec
// "Periodic Room Checkpoints"): the versioned checkpoint key scheme and the
// Sealer that MACs a checkpoint blob with a server-side key held outside the
// wasm sandbox. Checkpoint payloads (encode/decode, cadence, restore) live in
// the checkpoint track; this file owns only keys and integrity.
//
// Namespace note: these versioned keys (snapshots/<roomID>/<epoch>) share the
// "snapshots/" prefix — and the bucket lifecycle TTL on it (s3.go) — with the
// existing FLAT hibernation key snapshots/<roomID> (internal/gameabi
// HibernationStore.key). The two schemes coexist deliberately in Phase 0; Track
// C / task G.5 unifies room durability onto this versioned scheme and retires
// the flat key. A roomID is a UUIDv7, so snapshots/<roomID>/<epoch> can never
// collide with the flat snapshots/<roomID> object (one ends at the id, the
// other has a trailing "/epoch" segment).

// CheckpointKey returns the blobstore key for a room's checkpoint at the given
// epoch: snapshots/<roomID>/<epoch>. The epoch is zero-padded to 20 digits (the
// width of a uint64's max decimal value) so the lexical key order returned by
// Store.List matches numeric epoch order — checkpoints sort oldest-to-newest.
// Keys are written never-overwrite-in-place so a slow in-flight periodic PUT
// cannot clobber a later drain PUT at a higher epoch.
//
// epoch MUST be >= 0: a negative epoch would emit a leading '-' that breaks the
// lexical = numeric ordering invariant, so a negative epoch is a programmer
// error and panics (epochs are monotonic from 0).
func CheckpointKey(roomID string, epoch int64) string {
	if epoch < 0 {
		panic(fmt.Sprintf("blobstore: CheckpointKey: negative epoch %d", epoch))
	}
	return fmt.Sprintf("snapshots/%s/%020d", roomID, epoch)
}

// LatestPointerKey returns the key of a room's atomic latest-checkpoint pointer:
// snapshots/<roomID>/latest. The pointer is swapped atomically to the newest
// epoch; "latest" sorts after every zero-padded numeric epoch so it never
// shadows a checkpoint under the room prefix.
func LatestPointerKey(roomID string) string {
	return fmt.Sprintf("snapshots/%s/latest", roomID)
}

// ErrSealVerify is returned by Sealer.Open when a sealed blob's MAC does not
// verify (tampered payload, tampered MAC, wrong key, or truncated blob). A
// failed verification MUST refuse the restore before any guest-memory write.
var ErrSealVerify = errors.New("blobstore: checkpoint seal verification failed")

// Sealer authenticates checkpoint blobs with a server-side key held outside the
// wasm sandbox: artifact-digest equality is NOT blob integrity. Seal produces a
// blob that Open verifies before returning any payload, so a re-hydration
// always proves integrity before writing guest memory.
type Sealer interface {
	// Seal returns payload with an integrity tag appended.
	Seal(payload []byte) []byte
	// Open verifies the tag and returns the original payload, or ErrSealVerify.
	Open(sealed []byte) ([]byte, error)
}

// hmacSealer is the HMAC-SHA256 Sealer: it appends a 32-byte MAC over the
// payload and verifies it in constant time on Open.
type hmacSealer struct {
	key []byte
}

// NewHMACSealer returns a Sealer that MACs blobs with key using HMAC-SHA256.
func NewHMACSealer(key []byte) Sealer {
	cp := make([]byte, len(key))
	copy(cp, key)
	return &hmacSealer{key: cp}
}

const hmacTagLen = sha256.Size

func (s *hmacSealer) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(payload)
	return m.Sum(nil)
}

func (s *hmacSealer) Seal(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+hmacTagLen)
	out = append(out, payload...)
	out = append(out, s.mac(payload)...)
	return out
}

func (s *hmacSealer) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < hmacTagLen {
		return nil, ErrSealVerify
	}
	split := len(sealed) - hmacTagLen
	payload, tag := sealed[:split], sealed[split:]
	if !hmac.Equal(tag, s.mac(payload)) {
		return nil, ErrSealVerify
	}
	out := make([]byte, len(payload))
	copy(out, payload)
	return out, nil
}
