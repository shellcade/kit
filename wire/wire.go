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
	FnProfileGet      = "profile_get"
)

// Frame geometry: 80x24 cells, 24 bytes per v2 grapheme cell.
const (
	Rows       = 24
	Cols       = 80
	CellBytes  = 24
	FrameCells = Rows * Cols          // 1920
	FrameBytes = FrameCells * CellBytes // 46080
	RowBytes   = Cols * CellBytes     // 1920
)

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
}

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

// Err returns the decode error state.
func (r *Rd) Err() error {
	if r.Bad {
		return errors.New("wire: short or malformed payload")
	}
	return nil
}

// ---- CallContext ---------------------------------------------------------------

// EncodeCtx appends the packed CallContext to w.
func EncodeCtx(w *Buf, c Ctx) {
	w.I64(c.NowUnixNanos)
	w.I64(c.Seed)
	w.Bool(c.SeedSet)
	w.U8(c.Mode)
	w.U16(c.Capacity)
	w.U16(c.MinPlayers)
	w.U16(uint16(len(c.Members)))
	for _, p := range c.Members {
		w.Str(p.Handle)
		w.Str(p.AccountID)
		w.Str(p.Conn)
		w.U8(p.Kind)
	}
	w.Bool(c.Settled)
}

// DecodeCtx reads a CallContext, leaving r positioned at the event extras.
func DecodeCtx(r *Rd) Ctx {
	var c Ctx
	c.NowUnixNanos = r.I64()
	c.Seed = r.I64()
	c.SeedSet = r.Bool()
	c.Mode = r.U8()
	c.Capacity = r.U16()
	c.MinPlayers = r.U16()
	n := int(r.U16())
	for i := 0; i < n && !r.Bad; i++ {
		var p Player
		p.Handle = r.Str()
		p.AccountID = r.Str()
		p.Conn = r.Str()
		p.Kind = r.U8()
		c.Members = append(c.Members, p)
	}
	c.Settled = r.Bool()
	return c
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
	if err := r.Err(); err != nil {
		return Meta{}, err
	}
	if m.Slug == "" {
		return Meta{}, errors.New("wire: meta has empty slug")
	}
	return m, nil
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
	binary.LittleEndian.PutUint32(buf[o:], uint32(c.Rune))   // rune @0
	binary.LittleEndian.PutUint32(buf[o+4:], uint32(c.Cp2))  // cp2  @4
	binary.LittleEndian.PutUint32(buf[o+8:], uint32(c.Cp3))  // cp3  @8
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
