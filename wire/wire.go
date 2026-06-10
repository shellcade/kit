// Package wire IS the shellcade game ABI as code: the version handshake, the
// export and host-function names, and the packed little-endian payload
// encodings, expressed over neutral types with zero dependencies.
//
// This package is the single source of truth both sides compile against: the
// gamekit guest SDK maps wire types to its authoring types, and the private
// host adapter maps them to its engine types. Non-Go guests implement the same
// layouts from ABI.md, which documents exactly what this package encodes.
package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the ABI major version.
const Version uint32 = 2

// Guest export names.
const (
	ExpABI   = "shellcade_abi"
	ExpMeta  = "meta"
	ExpStart = "start"
	ExpJoin  = "join"
	ExpLeave = "leave"
	ExpInput = "input"
	ExpWake  = "wake"
	ExpClose = "close"
)

// HostNamespace is the wasm import namespace for shellcade host functions.
const HostNamespace = "extism:host/user"

// Host function names.
const (
	FnSend            = "send"
	FnIdentical       = "identical"
	FnSetInputContext = "set_input_context"
	FnEnd             = "end"
	FnPost            = "post"
	FnLog             = "log"
	FnKVGet           = "kv_get"
	FnKVSet           = "kv_set"
	FnKVDelete        = "kv_delete"
	FnConfigGet       = "config_get"
)

// Frame geometry: 80x24 cells, 24 bytes per v2 grapheme cell.
const (
	Rows       = 24
	Cols       = 80
	CellBytes  = 24
	FrameCells = Rows * Cols            // 1920
	FrameBytes = FrameCells * CellBytes // 46080
	RowBytes   = Cols * CellBytes       // 1920
)

// RosterCap is the contract-wide roster ceiling for per-index frame
// baselines: an SDK sizes its baseline table to RosterCap slots (plus the
// broadcast slot, conventionally at index RosterCap) and silently drops Send
// for a roster index >= RosterCap, and the host bounds-checks the send index
// and sizes its per-slot cache the same way (ABI.md §3, §4.6). 1024 since the
// large-room scale work (kit v2.5.0 Go / v2.7.0 Rust).
//
// This is a protocol invariant shared by every implementation — the Go guest
// SDK (internal/game), the Rust guest SDK (rust/src/broadcast.rs ROSTER_CAP,
// asserted equal to this constant by TestRustRosterCapMatchesWire in this
// package), and the host adapter — so changing it is ABI-affecting and must
// land in all of them in lockstep.
const RosterCap = 1024

// Player kind codes.
const (
	KindGuest  uint8 = 0
	KindMember uint8 = 1
)

// Mode codes.
const (
	ModeQuick   uint8 = 0
	ModePrivate uint8 = 1
	ModeSolo    uint8 = 2
)

// Status codes.
const (
	StatusFinished uint8 = 0
	StatusDNF      uint8 = 1
	StatusFlagged  uint8 = 2
)

// Input kind codes.
const (
	InputRune uint8 = 0
	InputKey  uint8 = 1
)

// Player is one roster entry.
type Player struct {
	Handle    string
	AccountID string
	Conn      string
	Kind      uint8
}

// Ctx is the CallContext every host→guest callback carries.
type Ctx struct {
	NowUnixNanos int64
	Seed         int64
	SeedSet      bool
	Mode         uint8
	Capacity     uint16
	MinPlayers   uint16
	Members      []Player
	Settled      bool

	// Roster-epoch mode (spec minor addition; emitted only to guests whose
	// meta declares CtxFeatRosterEpoch). RosterEpochSet marks a sentinel-form
	// member section: RosterUnchanged means the section carried only the
	// epoch (Members is nil — the guest reuses its cached roster); otherwise
	// Members is the full roster at RosterEpoch.
	RosterEpoch     uint32
	RosterEpochSet  bool
	RosterUnchanged bool
}

// Ctx member-section sentinels (roster-epoch mode). Real rosters are capped
// far below these values, so the count u16 disambiguates the three forms:
// 0..CtxRosterMaxCount = legacy full roster (no epoch), CtxRosterFull = u32
// epoch + u16 real count + members, CtxRosterUnchanged = u32 epoch only.
const (
	CtxRosterUnchanged uint16 = 0xFFFF
	CtxRosterFull      uint16 = 0xFFFE
	CtxRosterMaxCount  uint16 = 0xFFFD
)

// CtxFeatures bits a game may declare in its meta trailer. The host ignores
// bits it does not implement; the SDKs reject bits they do not define.
const (
	// CtxFeatRosterEpoch opts the guest into the ctx member-section sentinel
	// forms: full roster only on change (with an epoch), 6-byte unchanged
	// sections otherwise.
	CtxFeatRosterEpoch uint32 = 1 << 0

	// KnownCtxFeatures is the mask of bits this wire revision defines.
	KnownCtxFeatures uint32 = CtxFeatRosterEpoch
)

// Meta is the packed GameMeta.
type Meta struct {
	Slug              string
	Name              string
	ShortDescription  string
	MinPlayers        uint16
	MaxPlayers        uint16
	Tags              []string
	QuickModeLabel    string
	SoloModeLabel     string
	PrivateInviteLine string

	HasLeaderboard bool
	MetricLabel    string
	Direction      uint8
	Aggregation    uint8
	Format         uint8

	// ConfigSpecs is the trailing config-spec section (spec minor addition):
	// the game's declared admin-settable config keys. Encoders always write
	// the section (count 0 when empty); decoders treat a payload ending after
	// the leaderboard block as a valid pre-config meta with no specs.
	ConfigSpecs []ConfigSpec

	// CtxFeatures + HeartbeatMS form the trailing large-room section (spec
	// minor addition) after the config-spec section. CtxFeatures is the
	// negotiated-encoding bitset (CtxFeat*); HeartbeatMS is the game's
	// declared wake cadence (0 = no declaration; host precedence: admin
	// config > declaration > platform default, clamped to the platform
	// envelope). Encoders always write the section; decoders treat a payload
	// ending after the config-spec section as a valid older meta with zero
	// values.
	CtxFeatures uint32
	HeartbeatMS uint16

	// Lifecycle is the room end-of-life declaration (spec minor addition;
	// trailing byte after HeartbeatMS): 0 resumable (hibernate on abandon —
	// today's behavior and the zero-value default), 1 ephemeral (end +
	// dispose on abandon; no snapshot, no Resume entry), 2 resident (one
	// long-lived granted room per slug). Hosts treat values they do not
	// implement as resumable.
	Lifecycle uint8
}

// Lifecycle values for the meta trailer.
const (
	LifecycleResumable uint8 = 0
	LifecycleEphemeral uint8 = 1
	LifecycleResident  uint8 = 2
)

// Config value type codes (how the admin surface renders/validates a value).
const (
	ConfigText   uint8 = 0
	ConfigNumber uint8 = 1
	ConfigBool   uint8 = 2
	ConfigJSON   uint8 = 3
)

// ConfigSpec is one declared admin-settable config key in the meta payload.
type ConfigSpec struct {
	Key         string // the config_get key the game reads
	Title       string // short admin-facing label
	Description string // one-or-two-sentence admin help
	Type        uint8  // ConfigText..ConfigJSON
	Default     string // value the game uses when unset ("" = not declared)
	Schema      string // JSON Schema document (json type only; "" = none)
}

// Cell is one drawable cell of a frame. In v2 it carries up to three code
// points of a grapheme cluster: Rune is the base, Cp2/Cp3 the extra code
// points (0 = unused). The packed form is exactly 24 bytes (CellBytes), with
// the canonical-zero rule (unused cp slots and pad are zero) enforced by
// PutCell so cell equality is a 24-byte memcmp.
type Cell struct {
	Rune          rune
	Cp2           rune
	Cp3           rune
	FGSet         bool
	FGR, FGG, FGB uint8
	BGSet         bool
	BGR, BGG, BGB uint8
	Attr          uint8
	Cont          bool
}

// Ranking is one player's outcome in an end/post payload.
type Ranking struct {
	PlayerIdx uint32 // roster index in the callback's Ctx
	Metric    int64
	Rank      uint16
	Status    uint8
}

// Result is the end/post payload.
type Result struct {
	Rankings []Ranking
}

// ---- buffers -----------------------------------------------------------------

// Buf is a little-endian append-only encoder.
type Buf struct{ B []byte }

func (w *Buf) U8(v uint8)   { w.B = append(w.B, v) }
func (w *Buf) U16(v uint16) { w.B = binary.LittleEndian.AppendUint16(w.B, v) }
func (w *Buf) U32(v uint32) { w.B = binary.LittleEndian.AppendUint32(w.B, v) }
func (w *Buf) I64(v int64)  { w.B = binary.LittleEndian.AppendUint64(w.B, uint64(v)) }
func (w *Buf) Bool(v bool) {
	if v {
		w.U8(1)
	} else {
		w.U8(0)
	}
}
func (w *Buf) Str(s string) {
	if len(s) > 0xffff {
		s = s[:0xffff]
	}
	w.U16(uint16(len(s)))
	w.B = append(w.B, s...)
}

// Rd is a bounds-checked little-endian decoder.
type Rd struct {
	B   []byte
	Off int
	Bad bool
}

func (r *Rd) ok(n int) bool {
	if r.Bad || r.Off+n > len(r.B) {
		r.Bad = true
		return false
	}
	return true
}
func (r *Rd) U8() uint8 {
	if !r.ok(1) {
		return 0
	}
	v := r.B[r.Off]
	r.Off++
	return v
}
func (r *Rd) U16() uint16 {
	if !r.ok(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.B[r.Off:])
	r.Off += 2
	return v
}
func (r *Rd) U32() uint32 {
	if !r.ok(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.B[r.Off:])
	r.Off += 4
	return v
}
func (r *Rd) I64() int64 {
	if !r.ok(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.B[r.Off:])
	r.Off += 8
	return int64(v)
}
func (r *Rd) Bool() bool { return r.U8() == 1 }
func (r *Rd) Str() string {
	n := int(r.U16())
	if !r.ok(n) {
		return ""
	}
	s := string(r.B[r.Off : r.Off+n])
	r.Off += n
	return s
}

// SkipStr advances past one length-prefixed string without materializing it —
// the allocation-free skim used by the SDK's roster cache to find the member
// section's extent before deciding whether to decode it.
func (r *Rd) SkipStr() {
	n := int(r.U16())
	if !r.ok(n) {
		return
	}
	r.Off += n
}

// Err returns the decode error state.
func (r *Rd) Err() error {
	if r.Bad {
		return errors.New("wire: short or malformed payload")
	}
	return nil
}

// ---- CallContext ---------------------------------------------------------------

// EncodeCtx appends the packed CallContext to w in the LEGACY form (full
// roster, no epoch) — the only form pre-roster-epoch guests understand, and
// byte-identical to all prior wire revisions.
func EncodeCtx(w *Buf, c Ctx) {
	encodeCtxHeader(w, c)
	w.U16(uint16(len(c.Members)))
	encodeCtxMembers(w, c.Members)
	w.Bool(c.Settled)
}

// EncodeCtxEpoch appends the packed CallContext in roster-epoch mode (only
// for guests whose meta declares CtxFeatRosterEpoch). full=true emits the
// CtxRosterFull sentinel (epoch + real count + members); full=false emits
// the CtxRosterUnchanged sentinel (epoch only — the member section is 6
// bytes regardless of roster size, and c.Members is not read).
func EncodeCtxEpoch(w *Buf, c Ctx, epoch uint32, full bool) {
	encodeCtxHeader(w, c)
	if full {
		w.U16(CtxRosterFull)
		w.U32(epoch)
		w.U16(uint16(len(c.Members)))
		encodeCtxMembers(w, c.Members)
	} else {
		w.U16(CtxRosterUnchanged)
		w.U32(epoch)
	}
	w.Bool(c.Settled)
}

func encodeCtxHeader(w *Buf, c Ctx) {
	w.I64(c.NowUnixNanos)
	w.I64(c.Seed)
	w.Bool(c.SeedSet)
	w.U8(c.Mode)
	w.U16(c.Capacity)
	w.U16(c.MinPlayers)
}

func encodeCtxMembers(w *Buf, members []Player) {
	for _, p := range members {
		w.Str(p.Handle)
		w.Str(p.AccountID)
		w.Str(p.Conn)
		w.U8(p.Kind)
	}
}

// DecodeCtx reads a CallContext, leaving r positioned at the event extras.
// It recognises all three member-section forms; on the unchanged sentinel
// Members is nil and RosterUnchanged is true (the caller supplies its cached
// roster).
func DecodeCtx(r *Rd) Ctx {
	var c Ctx
	c.NowUnixNanos = r.I64()
	c.Seed = r.I64()
	c.SeedSet = r.Bool()
	c.Mode = r.U8()
	c.Capacity = r.U16()
	c.MinPlayers = r.U16()
	count := r.U16()
	switch count {
	case CtxRosterUnchanged:
		c.RosterEpoch = r.U32()
		c.RosterEpochSet = true
		c.RosterUnchanged = true
	case CtxRosterFull:
		c.RosterEpoch = r.U32()
		c.RosterEpochSet = true
		c.Members = decodeCtxMembers(r, int(r.U16()))
	default:
		c.Members = decodeCtxMembers(r, int(count))
	}
	c.Settled = r.Bool()
	return c
}

func decodeCtxMembers(r *Rd, n int) []Player {
	var members []Player
	for i := 0; i < n && !r.Bad; i++ {
		var p Player
		p.Handle = r.Str()
		p.AccountID = r.Str()
		p.Conn = r.Str()
		p.Kind = r.U8()
		members = append(members, p)
	}
	return members
}

// ---- GameMeta -------------------------------------------------------------------

// EncodeMeta packs a Meta.
func EncodeMeta(m Meta) []byte {
	var w Buf
	w.Str(m.Slug)
	w.Str(m.Name)
	w.Str(m.ShortDescription)
	w.U16(m.MinPlayers)
	w.U16(m.MaxPlayers)
	w.U16(uint16(len(m.Tags)))
	for _, t := range m.Tags {
		w.Str(t)
	}
	w.Str(m.QuickModeLabel)
	w.Str(m.SoloModeLabel)
	w.Str(m.PrivateInviteLine)
	w.Bool(m.HasLeaderboard)
	if m.HasLeaderboard {
		w.Str(m.MetricLabel)
		w.U8(m.Direction)
		w.U8(m.Aggregation)
		w.U8(m.Format)
	}
	// Trailing config-spec section (spec minor addition). Always written, so
	// a freshly encoded meta round-trips field-exact; decoders that predate
	// the section ignore trailing bytes.
	w.U16(uint16(len(m.ConfigSpecs)))
	for _, cs := range m.ConfigSpecs {
		w.Str(cs.Key)
		w.Str(cs.Title)
		w.Str(cs.Description)
		w.U8(cs.Type)
		w.Str(cs.Default)
		w.Str(cs.Schema)
	}
	// Trailing large-room section (spec minor addition): ctx-features bitset
	// + declared heartbeat. Always written; older decoders ignore the bytes.
	w.U32(m.CtxFeatures)
	w.U16(m.HeartbeatMS)
	// Trailing lifecycle byte (spec minor addition). Always written; older
	// decoders ignore it.
	w.U8(m.Lifecycle)
	return w.B
}

// DecodeMeta parses a packed Meta.
func DecodeMeta(b []byte) (Meta, error) {
	r := &Rd{B: b}
	var m Meta
	m.Slug = r.Str()
	m.Name = r.Str()
	m.ShortDescription = r.Str()
	m.MinPlayers = r.U16()
	m.MaxPlayers = r.U16()
	n := int(r.U16())
	for i := 0; i < n && !r.Bad; i++ {
		m.Tags = append(m.Tags, r.Str())
	}
	m.QuickModeLabel = r.Str()
	m.SoloModeLabel = r.Str()
	m.PrivateInviteLine = r.Str()
	m.HasLeaderboard = r.Bool()
	if m.HasLeaderboard {
		m.MetricLabel = r.Str()
		m.Direction = r.U8()
		m.Aggregation = r.U8()
		m.Format = r.U8()
	}
	// Trailing config-spec section, presence-guarded: a payload that ends
	// here is a valid pre-config meta with no declared specs.
	if !r.Bad && r.Off < len(r.B) {
		n := int(r.U16())
		for i := 0; i < n && !r.Bad; i++ {
			var cs ConfigSpec
			cs.Key = r.Str()
			cs.Title = r.Str()
			cs.Description = r.Str()
			cs.Type = r.U8()
			cs.Default = r.Str()
			cs.Schema = r.Str()
			m.ConfigSpecs = append(m.ConfigSpecs, cs)
		}
	}
	// Trailing large-room section, presence-guarded: a payload that ends
	// here is a valid older meta declaring no ctx features and no heartbeat.
	if !r.Bad && r.Off < len(r.B) {
		m.CtxFeatures = r.U32()
		m.HeartbeatMS = r.U16()
	}
	// Trailing lifecycle byte, presence-guarded: absent = resumable.
	if !r.Bad && r.Off < len(r.B) {
		m.Lifecycle = r.U8()
	}
	if err := r.Err(); err != nil {
		return Meta{}, err
	}
	if m.Slug == "" {
		return Meta{}, errors.New("wire: meta has empty slug")
	}
	return m, nil
}

// HostKeyPrefix is the reserved config-key namespace interpreted by the host
// (e.g. host.heartbeat_ms). Games MUST NOT declare specs under it — the
// platform declares those knobs itself.
const HostKeyPrefix = "host."

// ValidateConfigSpecs enforces the authoring rules for declared config specs,
// shared by guest SDK encoders and host/CLI decoders: keys non-empty and
// unique, no reserved host. prefix, a known type code, and Schema only on
// JSON-typed keys where it must itself parse as JSON. The JSON check is a
// well-formedness scan (json.Valid) — schema COMPILATION is a host concern,
// keeping this package dependency-free.
func ValidateConfigSpecs(specs []ConfigSpec) error {
	seen := make(map[string]bool, len(specs))
	for _, cs := range specs {
		if cs.Key == "" {
			return errors.New("wire: config spec has an empty key")
		}
		if seen[cs.Key] {
			return fmt.Errorf("wire: duplicate config spec key %q", cs.Key)
		}
		seen[cs.Key] = true
		if len(cs.Key) >= len(HostKeyPrefix) && cs.Key[:len(HostKeyPrefix)] == HostKeyPrefix {
			return fmt.Errorf("wire: config spec key %q uses the reserved %q prefix", cs.Key, HostKeyPrefix)
		}
		if cs.Type > ConfigJSON {
			return fmt.Errorf("wire: config spec %q has unknown type %d", cs.Key, cs.Type)
		}
		if cs.Schema != "" {
			if cs.Type != ConfigJSON {
				return fmt.Errorf("wire: config spec %q declares a schema on a non-json type", cs.Key)
			}
			if !json.Valid([]byte(cs.Schema)) {
				return fmt.Errorf("wire: config spec %q schema is not valid JSON", cs.Key)
			}
		}
	}
	return nil
}

// Heartbeat declaration envelope (mirrors the host's clamp range).
const (
	HeartbeatMinMS uint16 = 20
	HeartbeatMaxMS uint16 = 1000
)

// ValidateLifecycle is the shared authoring rule set for the lifecycle
// declaration, enforced at meta() encode time by both SDKs: the value must
// be a defined lifecycle, and resident cannot be combined with
// minPlayers > 1 (a resident room runs with zero members).
func ValidateLifecycle(lifecycle uint8, minPlayers uint16) error {
	if lifecycle > LifecycleResident {
		return fmt.Errorf("wire: Lifecycle %d undefined (0 resumable, 1 ephemeral, 2 resident)", lifecycle)
	}
	if lifecycle == LifecycleResident && minPlayers > 1 {
		return fmt.Errorf("wire: Lifecycle resident cannot require MinPlayers %d — a resident room runs with zero members", minPlayers)
	}
	return nil
}

// ValidateMetaTrailer is the shared authoring rule set for the large-room
// meta section, enforced at meta() encode time by both SDKs (the same
// fail-fast posture as ValidateConfigSpecs): no undefined ctx-feature bits;
// heartbeat 0 (no declaration) or within [HeartbeatMinMS, HeartbeatMaxMS].
func ValidateMetaTrailer(ctxFeatures uint32, heartbeatMS uint16) error {
	if unknown := ctxFeatures &^ KnownCtxFeatures; unknown != 0 {
		return fmt.Errorf("wire: CtxFeatures declares undefined bit(s) %#x", unknown)
	}
	if heartbeatMS != 0 && (heartbeatMS < HeartbeatMinMS || heartbeatMS > HeartbeatMaxMS) {
		return fmt.Errorf("wire: HeartbeatMS %d outside 0 or [%d,%d]", heartbeatMS, HeartbeatMinMS, HeartbeatMaxMS)
	}
	return nil
}

// ---- Frames ----------------------------------------------------------------------

// PutCell writes one cell at index i (0..FrameCells-1) into a FrameBytes buffer
// using the v2 24-byte anchor layout:
//
//	rune@0  cp2@4  cp3@8  fg@12..15  bg@16..19  attr@20  cont@21  pad@22..23
//
// PutCell is the normative CANONICAL-ZERO enforcer: it always writes pad = 0
// and writes whatever cp2/cp3 the cell carries (0 = unused), so even a
// hand-built Cell with garbage left in a slot it should not use serializes
// canonically — cell equality is then exactly a 24-byte memcmp, which is
// load-bearing for delta determinism and hibernation byte-identity.
func PutCell(buf []byte, i int, c Cell) {
	o := i * CellBytes
	binary.LittleEndian.PutUint32(buf[o:], uint32(c.Rune))  // rune @0
	binary.LittleEndian.PutUint32(buf[o+4:], uint32(c.Cp2)) // cp2  @4
	binary.LittleEndian.PutUint32(buf[o+8:], uint32(c.Cp3)) // cp3  @8
	if c.FGSet {
		buf[o+12], buf[o+13], buf[o+14], buf[o+15] = 1, c.FGR, c.FGG, c.FGB
	} else {
		buf[o+12], buf[o+13], buf[o+14], buf[o+15] = 0, 0, 0, 0
	}
	if c.BGSet {
		buf[o+16], buf[o+17], buf[o+18], buf[o+19] = 1, c.BGR, c.BGG, c.BGB
	} else {
		buf[o+16], buf[o+17], buf[o+18], buf[o+19] = 0, 0, 0, 0
	}
	buf[o+20] = c.Attr
	if c.Cont {
		buf[o+21] = 1
	} else {
		buf[o+21] = 0
	}
	buf[o+22], buf[o+23] = 0, 0 // pad (canonical zero)
}

// GetCell reads one cell at index i from a FrameBytes buffer (24-byte layout).
func GetCell(buf []byte, i int) Cell {
	o := i * CellBytes
	var c Cell
	c.Rune = rune(binary.LittleEndian.Uint32(buf[o:]))
	c.Cp2 = rune(binary.LittleEndian.Uint32(buf[o+4:]))
	c.Cp3 = rune(binary.LittleEndian.Uint32(buf[o+8:]))
	c.FGSet = buf[o+12] == 1
	c.FGR, c.FGG, c.FGB = buf[o+13], buf[o+14], buf[o+15]
	c.BGSet = buf[o+16] == 1
	c.BGR, c.BGG, c.BGB = buf[o+17], buf[o+18], buf[o+19]
	c.Attr = buf[o+20]
	c.Cont = buf[o+21] == 1
	return c
}

// CheckFrame validates a full-frame payload length (the bare packed grid; used
// by the host-side baseline buffers, not the wire send path, which carries the
// delta container instead — see CheckFrameDelta).
func CheckFrame(b []byte) error {
	if len(b) != FrameBytes {
		return fmt.Errorf("wire: frame payload is %d bytes, want %d", len(b), FrameBytes)
	}
	return nil
}

// ---- Results ----------------------------------------------------------------------

// EncodeResult packs a Result.
func EncodeResult(res Result) []byte {
	var w Buf
	w.U16(uint16(len(res.Rankings)))
	for _, rk := range res.Rankings {
		w.U32(rk.PlayerIdx)
		w.I64(rk.Metric)
		w.U16(rk.Rank)
		w.U8(rk.Status)
	}
	return w.B
}

// DecodeResult parses a packed Result.
func DecodeResult(b []byte) (Result, error) {
	r := &Rd{B: b}
	var res Result
	n := int(r.U16())
	for i := 0; i < n && !r.Bad; i++ {
		res.Rankings = append(res.Rankings, Ranking{
			PlayerIdx: r.U32(),
			Metric:    r.I64(),
			Rank:      r.U16(),
			Status:    r.U8(),
		})
	}
	return res, r.Err()
}
