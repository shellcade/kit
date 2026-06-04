package gamekit

import "encoding/binary"

// Guest-side mirror of the ABI v1 packed little-endian encodings.

type wbuf struct{ b []byte }

func (w *wbuf) u8(v uint8)   { w.b = append(w.b, v) }
func (w *wbuf) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *wbuf) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *wbuf) i64(v int64)  { w.b = binary.LittleEndian.AppendUint64(w.b, uint64(v)) }
func (w *wbuf) str(s string) {
	if len(s) > 0xffff {
		s = s[:0xffff]
	}
	w.u16(uint16(len(s)))
	w.b = append(w.b, s...)
}

type rbuf struct {
	b   []byte
	off int
	bad bool
}

func (r *rbuf) ok(n int) bool {
	if r.bad || r.off+n > len(r.b) {
		r.bad = true
		return false
	}
	return true
}
func (r *rbuf) u8() uint8 {
	if !r.ok(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}
func (r *rbuf) u16() uint16 {
	if !r.ok(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}
func (r *rbuf) u32() uint32 {
	if !r.ok(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}
func (r *rbuf) i64() int64 {
	if !r.ok(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return int64(v)
}
func (r *rbuf) str() string {
	n := int(r.u16())
	if !r.ok(n) {
		return ""
	}
	s := string(r.b[r.off : r.off+n])
	r.off += n
	return s
}

// callContext is the decoded per-callback room state.
type callContext struct {
	nowUnixNanos int64
	cfg          RoomConfig
	members      []Player
	settled      bool
}

// decodeCtx decodes a CallContext and returns the reader positioned at the
// event-specific extra payload.
func decodeCtx(b []byte) (callContext, *rbuf) {
	r := &rbuf{b: b}
	var c callContext
	c.nowUnixNanos = r.i64()
	c.cfg.Seed = r.i64()
	c.cfg.SeedSet = r.u8() == 1
	c.cfg.Mode = Mode(r.u8())
	c.cfg.Capacity = int(r.u16())
	c.cfg.MinPlayers = int(r.u16())
	n := int(r.u16())
	for i := 0; i < n && !r.bad; i++ {
		var p Player
		p.Handle = r.str()
		p.AccountID = r.str()
		p.Conn = r.str()
		p.Kind = Kind(r.u8())
		c.members = append(c.members, p)
	}
	c.settled = r.u8() == 1
	return c, r
}

// encodeMeta packs GameMeta for the meta export.
func encodeMeta(m GameMeta) []byte {
	var w wbuf
	w.str(m.Slug)
	w.str(m.Name)
	w.str(m.ShortDescription)
	w.u16(uint16(m.MinPlayers))
	w.u16(uint16(m.MaxPlayers))
	w.u16(uint16(len(m.Tags)))
	for _, t := range m.Tags {
		w.str(t)
	}
	w.str(m.QuickModeLabel)
	w.str(m.SoloModeLabel)
	w.str(m.PrivateInviteLine)
	if m.Leaderboard != nil {
		w.u8(1)
		w.str(m.Leaderboard.MetricLabel)
		w.u8(uint8(m.Leaderboard.Direction))
		w.u8(uint8(m.Leaderboard.Aggregation))
		w.u8(uint8(m.Leaderboard.Format))
	} else {
		w.u8(0)
	}
	return w.b
}

// frameScratch is the reused frame encode buffer: one room per instance and
// serial callbacks mean no concurrent encodes, and reuse keeps the steady
// state allocation-free (important under TinyGo's GC).
var frameScratch [Rows * Cols * 16]byte

// encodeFrame packs a Frame as the 16-byte-per-cell array into the scratch
// buffer (valid until the next encodeFrame call).
func encodeFrame(f *Frame) []byte {
	i := 0
	for row := 0; row < Rows; row++ {
		for col := 0; col < Cols; col++ {
			c := &f.Cells[row][col]
			binary.LittleEndian.PutUint32(frameScratch[i:], uint32(c.Rune))
			if c.FG.set {
				frameScratch[i+4], frameScratch[i+5], frameScratch[i+6], frameScratch[i+7] = 1, c.FG.r, c.FG.g, c.FG.b
			} else {
				frameScratch[i+4], frameScratch[i+5], frameScratch[i+6], frameScratch[i+7] = 0, 0, 0, 0
			}
			if c.BG.set {
				frameScratch[i+8], frameScratch[i+9], frameScratch[i+10], frameScratch[i+11] = 1, c.BG.r, c.BG.g, c.BG.b
			} else {
				frameScratch[i+8], frameScratch[i+9], frameScratch[i+10], frameScratch[i+11] = 0, 0, 0, 0
			}
			frameScratch[i+12] = uint8(c.Attr)
			if c.Cont {
				frameScratch[i+13] = 1
			} else {
				frameScratch[i+13] = 0
			}
			frameScratch[i+14], frameScratch[i+15] = 0, 0
			i += 16
		}
	}
	return frameScratch[:]
}

// encodeResult packs a Result against the current roster (player -> index).
func encodeResult(res Result, roster []Player) []byte {
	var w wbuf
	w.u16(uint16(len(res.Rankings)))
	for _, pr := range res.Rankings {
		idx := 0
		for i, p := range roster {
			if p == pr.Player {
				idx = i
				break
			}
		}
		w.u32(uint32(idx))
		w.i64(int64(pr.Metric))
		w.u16(uint16(pr.Rank))
		w.u8(uint8(pr.Status))
	}
	return w.b
}
