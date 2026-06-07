package wire

import (
	"bytes"
	"testing"
)

func lrPlayers() []Player {
	return []Player{
		{Handle: "ada", AccountID: "a", Conn: "c1", Kind: 1},
		{Handle: "bob", AccountID: "b", Conn: "c2", Kind: 1},
	}
}

func lrCtx() Ctx {
	return Ctx{NowUnixNanos: 9, Seed: 7, SeedSet: true, Mode: 0, Capacity: 1000, MinPlayers: 1, Members: lrPlayers()}
}

// The full sentinel form round-trips members + epoch and leaves the reader at
// the event extras.
func TestCtxEpochFullRoundTrip(t *testing.T) {
	var w Buf
	EncodeCtxEpoch(&w, lrCtx(), 42, true)
	w.U8(0xAB) // event extra

	r := &Rd{B: w.B}
	c := DecodeCtx(r)
	if !c.RosterEpochSet || c.RosterUnchanged || c.RosterEpoch != 42 {
		t.Fatalf("full form decoded epoch=%d set=%v unchanged=%v", c.RosterEpoch, c.RosterEpochSet, c.RosterUnchanged)
	}
	if len(c.Members) != 2 || c.Members[1].AccountID != "b" {
		t.Fatalf("members = %+v", c.Members)
	}
	if got := r.U8(); got != 0xAB || r.Bad {
		t.Fatalf("event extras misaligned: got %#x bad=%v", got, r.Bad)
	}
}

// The unchanged sentinel form carries only the epoch (6-byte member section)
// and leaves the reader at the event extras.
func TestCtxEpochUnchangedRoundTrip(t *testing.T) {
	var w Buf
	EncodeCtxEpoch(&w, lrCtx(), 43, false)
	w.U8(0xCD)

	r := &Rd{B: w.B}
	c := DecodeCtx(r)
	if !c.RosterEpochSet || !c.RosterUnchanged || c.RosterEpoch != 43 {
		t.Fatalf("unchanged form decoded epoch=%d set=%v unchanged=%v", c.RosterEpoch, c.RosterEpochSet, c.RosterUnchanged)
	}
	if c.Members != nil {
		t.Fatalf("unchanged form decoded members: %+v", c.Members)
	}
	if got := r.U8(); got != 0xCD || r.Bad {
		t.Fatalf("event extras misaligned: got %#x bad=%v", got, r.Bad)
	}

	// The member section is exactly count(2) + epoch(4) = 6 bytes: the
	// unchanged encoding must not scale with roster size.
	var small, large Buf
	EncodeCtxEpoch(&small, Ctx{Members: nil}, 1, false)
	bigCtx := Ctx{Members: make([]Player, 500)}
	EncodeCtxEpoch(&large, bigCtx, 1, false)
	if len(small.B) != len(large.B) {
		t.Fatalf("unchanged form scales with roster: %d vs %d bytes", len(small.B), len(large.B))
	}
}

// The legacy form is byte-identical to the pre-sentinel encoding and decodes
// with no epoch fields set.
func TestCtxLegacyUnchangedBytes(t *testing.T) {
	var w Buf
	EncodeCtx(&w, lrCtx())

	// Hand-build the historical encoding.
	var h Buf
	h.I64(9)
	h.I64(7)
	h.Bool(true)
	h.U8(0)
	h.U16(1000)
	h.U16(1)
	h.U16(2)
	for _, p := range lrPlayers() {
		h.Str(p.Handle)
		h.Str(p.AccountID)
		h.Str(p.Conn)
		h.U8(p.Kind)
	}
	h.Bool(false)
	if !bytes.Equal(w.B, h.B) {
		t.Fatal("legacy EncodeCtx is not byte-identical to the historical encoding")
	}

	c := DecodeCtx(&Rd{B: w.B})
	if c.RosterEpochSet || c.RosterUnchanged {
		t.Fatalf("legacy decode set epoch fields: %+v", c)
	}
	if len(c.Members) != 2 {
		t.Fatalf("legacy decode members = %+v", c.Members)
	}
}

// The meta trailer round-trips, and a payload truncated before it decodes as
// an older meta with zero values.
func TestMetaTrailerRoundTrip(t *testing.T) {
	m := Meta{Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 1000,
		CtxFeatures: CtxFeatRosterEpoch, HeartbeatMS: 100}
	b := EncodeMeta(m)
	got, err := DecodeMeta(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CtxFeatures != CtxFeatRosterEpoch || got.HeartbeatMS != 100 {
		t.Fatalf("trailer round-trip = features %#x heartbeat %d", got.CtxFeatures, got.HeartbeatMS)
	}

	// Pre-trailer payload: chop the trailing 6 bytes (u32 + u16).
	old := b[:len(b)-6]
	got, err = DecodeMeta(old)
	if err != nil {
		t.Fatalf("pre-trailer decode: %v", err)
	}
	if got.CtxFeatures != 0 || got.HeartbeatMS != 0 {
		t.Fatalf("pre-trailer payload decoded nonzero trailer: %#x %d", got.CtxFeatures, got.HeartbeatMS)
	}
}

func TestValidateMetaTrailer(t *testing.T) {
	cases := []struct {
		features uint32
		hb       uint16
		ok       bool
	}{
		{0, 0, true},
		{CtxFeatRosterEpoch, 0, true},
		{CtxFeatRosterEpoch, 100, true},
		{0, 20, true},
		{0, 1000, true},
		{1 << 7, 0, false},  // undefined feature bit
		{0, 5, false},       // below envelope
		{0, 1500, false},    // above envelope
	}
	for _, c := range cases {
		err := ValidateMetaTrailer(c.features, c.hb)
		if (err == nil) != c.ok {
			t.Errorf("ValidateMetaTrailer(%#x, %d) err=%v, want ok=%v", c.features, c.hb, err, c.ok)
		}
	}
}
