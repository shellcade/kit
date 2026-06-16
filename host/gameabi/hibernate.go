package gameabi

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shellcade/kit/v2/host/blobstore"
	"github.com/shellcade/kit/v2/host/sdk"
)

// D9 hibernation, ABI tasks 6.2–6.5: the snapshot CODEC lives in snapshot.go;
// this file is the STORE (where a frozen room is parked + how it is listed for
// resume) and the capability glue (which handlers may be frozen). The codec is
// consumed only through export.go's SnapshotHandler/RestoreHandler — this file
// never reaches into wasmHandler/wasmGame internals beyond the capability check.

// CanHibernate implements sdk.HibernationCapable: a wasm room can be frozen when
// it holds a live instance that has not faulted, ended, or closed. The actor
// guarantees no callback is on the stack at the quiescent point Hibernate runs,
// so this is a pure state check.
func (h *wasmHandler) CanHibernate() bool {
	return h.inst != nil && !h.dead && !h.ended
}

// OnResume implements sdk.Resumed: a restored handler already holds a live,
// memory-restored instance (RestoreHandler built it), so resuming must NOT
// re-instantiate the way OnStart does — it only re-establishes the engine-owned
// heartbeat (the sim rate OnStart would normally set). The guest is not called;
// the next OnTick/OnInput continues exactly where the snapshot left off. A
// restored-but-dead handler (shouldn't happen — Restore returns an error
// instead) settles the room rather than running headless.
func (h *wasmHandler) OnResume(r sdk.Room) {
	if h.inst == nil || h.dead {
		r.End(sdk.Result{Mode: h.cfg.Mode})
		h.dead = true
		return
	}
	// D6 frame-delta resync: Restore already seeded the baseline cache's epoch
	// counter strictly above the snapshot's high-water and marked every slot
	// not-present. Re-assert not-present here (idempotent) so a reconnecting
	// viewer / new connection token also starts from a keyframe — the engine's
	// resume entry point owns this guarantee even if a future Restore path
	// changes. The cache is host memory and was never snapshotted.
	h.baselines.invalidateAll()
	r.SetSimRate(h.heartbeat)
}

// ---- hibernation store -------------------------------------------------------

// snapshotPrefix is the blobstore key namespace for parked rooms; the bucket's
// lifecycle rule (blobstore.EnsureSnapshotTTL) expires anything under it, so the
// store itself never has to age blobs out.
const snapshotPrefix = "snapshots/"

// Header is the small, UNCOMPRESSED descriptor prepended to a stored snapshot
// so the resume metadata (game, age, roster) is readable WITHOUT decompressing
// the zstd body or touching the wasm runtime. It is also the row shape of the
// Postgres parked-room directory (add-parked-room-directory) the production
// resume listing derives from. It deliberately duplicates a few fields the
// codec also carries (slug, roster) because the codec body is opaque to the
// store and only the host that owns the matching artifact can decode it — the
// lobby must list a member's parked rooms without loading every game.
//
// On-wire layout is fixed and self-describing (magic + format), length-prefixed,
// little-endian — decodable standalone, never via zstd:
//
//	u32  magic ("SCH1")
//	u32  format version
//	str  game slug
//	str  room id
//	i64  hibernated-at unix nanos
//	u16  roster length, then that many { str accountID; str handle }
//	... snapshot body (the opaque zstd codec blob) follows the header.
type Header struct {
	Slug   string         // game slug the snapshot belongs to (resume listing + game lookup)
	RoomID string         // the parked room's id (== the storage key suffix)
	At     time.Time      // when the room was hibernated (resume listing "age")
	Roster []RosterMember // who was in the room (resume listing "players" + membership filter)
}

// RosterMember is one parked player, enough to filter the resume list to the
// requesting member and to show who else was in the room.
type RosterMember struct {
	AccountID string
	Handle    string
}

const (
	headerMagic  = 0x53434831 // "SCH1"
	headerFormat = 1
)

// RosterFrom projects a roster of sdk.Players to header roster members.
func RosterFrom(roster []sdk.Player) []RosterMember {
	out := make([]RosterMember, 0, len(roster))
	for _, p := range roster {
		out = append(out, RosterMember{AccountID: p.AccountID, Handle: p.Handle})
	}
	return out
}

// Has reports whether accountID is in the parked roster.
func (h Header) Has(accountID string) bool {
	for _, m := range h.Roster {
		if m.AccountID == accountID {
			return true
		}
	}
	return false
}

// encodeHeader writes the fixed, uncompressed header. It is a hand-rolled
// little-endian framing (no dependency on the wasm wire codec) so the store
// stays independent of the ABI.
func encodeHeader(h Header) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, headerMagic)
	b = binary.LittleEndian.AppendUint32(b, headerFormat)
	b = appendStr(b, h.Slug)
	b = appendStr(b, h.RoomID)
	b = binary.LittleEndian.AppendUint64(b, uint64(h.At.UnixNano()))
	b = binary.LittleEndian.AppendUint16(b, uint16(len(h.Roster)))
	for _, m := range h.Roster {
		b = appendStr(b, m.AccountID)
		b = appendStr(b, m.Handle)
	}
	return b
}

// decodeHeader reads a header and returns it plus the length of the header
// region (so the caller can slice off the snapshot body that follows).
func decodeHeader(blob []byte) (Header, int, error) {
	r := reader{b: blob}
	if r.u32() != headerMagic {
		return Header{}, 0, fmt.Errorf("blobstore: snapshot header: bad magic")
	}
	if f := r.u32(); f != headerFormat {
		return Header{}, 0, fmt.Errorf("blobstore: snapshot header: format v%d, want v%d", f, headerFormat)
	}
	var h Header
	h.Slug = r.str()
	h.RoomID = r.str()
	h.At = time.Unix(0, r.i64())
	n := int(r.u16())
	for i := 0; i < n; i++ {
		m := RosterMember{AccountID: r.str(), Handle: r.str()}
		h.Roster = append(h.Roster, m)
	}
	if r.err != nil {
		return Header{}, 0, fmt.Errorf("blobstore: snapshot header: %w", r.err)
	}
	return h, r.off, nil
}

// HibernationStore parks frozen rooms in a blobstore.Store under snapshots/. A
// stored blob is the uncompressed Header followed by the opaque snapshot body;
// Get returns the body for restore, and a successful restore Deletes the blob
// (TTL is the bucket's backstop). List exists ONLY for the directory-less
// legacy path (see its doc): the production resume listing derives from the
// Postgres parked-room directory (add-parked-room-directory), never from blob
// enumeration. All ops are context-bound and concurrency-safe (the backing
// Store is).
//
// INTEGRITY: when constructed with a Sealer, Put MACs the whole header+body
// blob and Get/List verify it BEFORE decoding the header — a restored snapshot
// writes the roster and raw guest linear memory into the host, so an
// unauthenticated blob is a direct write-primitive for anyone who can write
// the bucket, and the header roster alone gates whose Resume menu a parked
// room appears in. This is the same HMAC Sealer (same server-side key
// convention) the versioned checkpoint scheme uses. A nil Sealer keeps the
// legacy unsealed layout for keyless dev/test stores.
type HibernationStore struct {
	store  blobstore.Store
	sealer blobstore.Sealer
}

// NewHibernationStore wraps a blobstore.Store. A nil store yields a no-op-safe
// zero value? No — callers must pass a real store; nil is a programmer error and
// every method guards it by returning an error rather than panicking. sealer
// authenticates parked blobs (see the type doc); nil means unsealed — only
// acceptable when no server-side MAC key exists (keyless dev mode, in-process
// test rigs).
//
// MIGRATION (clean cutover): a sealed store REJECTS blobs parked unsealed by an
// older binary (their trailing bytes are not a MAC), discarding them through
// the corrupt-blob path. Grandfathering unsealed blobs was deliberately NOT
// implemented — it would let a bucket writer bypass the MAC by stripping it,
// and the bucket's 14-day snapshot TTL bounds the loss to rooms parked at
// deploy time, the same loss policy as prior snapshot format bumps (v2→v3,
// v3→v4 hard-reject).
func NewHibernationStore(store blobstore.Store, sealer blobstore.Sealer) *HibernationStore {
	return &HibernationStore{store: store, sealer: sealer}
}

// key is the storage key for a room id: the FLAT hibernation key
// snapshots/<roomID>. Namespace note: this shares the "snapshots/" prefix with
// the versioned room-checkpoint scheme snapshots/<roomID>/<epoch>
// (blobstore.CheckpointKey). The two coexist deliberately in Phase 0; Track C /
// task G.5 unifies durability onto the versioned scheme and retires this flat
// key. A roomID is a UUIDv7, so the flat key never collides with a versioned
// one (the latter has a trailing "/epoch" segment).
func (s *HibernationStore) key(roomID string) string { return snapshotPrefix + roomID }

// Put parks a snapshot: it prepends the header to the body, seals the whole
// blob when a Sealer is wired, and writes it at snapshots/<header.RoomID>. The
// header's RoomID is authoritative for the key.
func (s *HibernationStore) Put(ctx context.Context, h Header, body []byte) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("blobstore: hibernation store not configured")
	}
	if h.RoomID == "" {
		return fmt.Errorf("blobstore: hibernate: empty room id")
	}
	blob := append(encodeHeader(h), body...)
	if s.sealer != nil {
		blob = s.sealer.Seal(blob)
	}
	return s.store.Put(ctx, s.key(h.RoomID), blob)
}

// open verifies and strips the seal when a Sealer is wired; with no Sealer the
// blob passes through unchanged (legacy unsealed layout). The error wraps
// blobstore.ErrSealVerify so callers can route it to the discard path.
func (s *HibernationStore) open(blob []byte) ([]byte, error) {
	if s.sealer == nil {
		return blob, nil
	}
	payload, err := s.sealer.Open(blob)
	if err != nil {
		return nil, fmt.Errorf("blobstore: hibernation snapshot: %w", err)
	}
	return payload, nil
}

// List returns the header of every parked room, newest first.
//
// COST: this is NOT cheap. blobstore.Store.Get returns the WHOLE object, so
// List downloads every blob under snapshots/ — including every versioned room
// checkpoint sharing the prefix (snapshots/<roomID>/<epoch>, headerless, fully
// fetched only to fail the magic check below) — just to decode the small
// headers. It is retained ONLY for hibernators constructed without a Postgres
// parked-room directory (in-process test rigs over blobstore.Memory); the
// production resume listing is a per-account directory query
// (add-parked-room-directory) and never calls this.
//
// Corrupt or foreign blobs under snapshots/ are skipped (logged by the caller
// if it cares), never fatal — a single bad object must not hide the rest. With
// a Sealer wired, a blob that fails seal verification is skipped the same way
// BEFORE its header is decoded: the header roster gates Resume-list visibility
// (Header.Has), so an unverified header must never reach a listing.
func (s *HibernationStore) List(ctx context.Context) ([]Header, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("blobstore: hibernation store not configured")
	}
	keys, err := s.store.List(ctx, snapshotPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]Header, 0, len(keys))
	for _, k := range keys {
		if !strings.HasPrefix(k, snapshotPrefix) {
			continue
		}
		blob, ok, err := s.store.Get(ctx, k)
		if err != nil || !ok {
			continue
		}
		payload, err := s.open(blob)
		if err != nil {
			continue // unverifiable (tampered/unsealed/foreign) — never list it
		}
		h, _, err := decodeHeader(payload)
		if err != nil {
			continue // skip a corrupt/foreign object rather than failing the list
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// Get returns the parked room's header and snapshot body (the opaque codec blob
// to feed RestoreHandler). ok=false when no snapshot exists for roomID. With a
// Sealer wired, the seal is verified BEFORE the header is decoded; a failure
// returns an error wrapping blobstore.ErrSealVerify and the caller MUST refuse
// the restore (no guest memory is written) and discard the blob like any other
// corrupt snapshot.
func (s *HibernationStore) Get(ctx context.Context, roomID string) (Header, []byte, bool, error) {
	if s == nil || s.store == nil {
		return Header{}, nil, false, fmt.Errorf("blobstore: hibernation store not configured")
	}
	blob, ok, err := s.store.Get(ctx, s.key(roomID))
	if err != nil || !ok {
		return Header{}, nil, false, err
	}
	payload, err := s.open(blob)
	if err != nil {
		return Header{}, nil, false, err
	}
	h, n, err := decodeHeader(payload)
	if err != nil {
		return Header{}, nil, false, err
	}
	body := make([]byte, len(payload)-n)
	copy(body, payload[n:])
	return h, body, true, nil
}

// Delete removes a parked room (called on a successful restore, and to discard a
// failed/corrupt snapshot). Deleting a missing room is not an error.
func (s *HibernationStore) Delete(ctx context.Context, roomID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("blobstore: hibernation store not configured")
	}
	return s.store.Delete(ctx, s.key(roomID))
}

// ---- little-endian framing helpers (header only) -----------------------------

func appendStr(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

// reader is a bounds-checked little-endian reader; on overrun it latches err and
// every further read yields a zero value, so decodeHeader can check err once.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) ok(n int) bool {
	if r.err != nil || r.off+n > len(r.b) {
		if r.err == nil {
			r.err = fmt.Errorf("unexpected end of header")
		}
		return false
	}
	return true
}

func (r *reader) u16() uint16 {
	if !r.ok(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.ok(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) i64() int64 {
	if !r.ok(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return int64(v)
}

func (r *reader) str() string {
	n := int(r.u16())
	if !r.ok(n) {
		return ""
	}
	s := string(r.b[r.off : r.off+n])
	r.off += n
	return s
}
