package wire

import (
	"bytes"
	"reflect"
	"testing"
)

func TestCtxRoundTrip(t *testing.T) {
	in := Ctx{
		NowUnixNanos: 1234567890123456789,
		Seed:         42, SeedSet: true,
		Mode: ModeSolo, Capacity: 5, MinPlayers: 1,
		Members: []Player{
			{Handle: "alice", AccountID: "a-1", Conn: "c-1", Kind: KindMember},
			{Handle: "bob", AccountID: "b-2", Conn: "c-2", Kind: KindGuest},
		},
		Settled: true,
	}
	var w Buf
	EncodeCtx(&w, in)
	r := &Rd{B: w.B}
	out := DecodeCtx(r)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
	if r.Off != len(w.B) {
		t.Fatalf("decoder left %d trailing bytes", len(w.B)-r.Off)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	in := Meta{
		Slug: "pokies", Name: "Pokies", ShortDescription: "spin to win",
		MinPlayers: 1, MaxPlayers: 5,
		Tags:           []string{"slots", "casual"},
		QuickModeLabel: "Quick spin", SoloModeLabel: "Solo spin", PrivateInviteLine: "join the floor",
		HasLeaderboard: true, MetricLabel: "Credits", Direction: 0, Aggregation: 0, Format: 0,
	}
	out, err := DecodeMeta(EncodeMeta(in))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestMetaConfigSpecsRoundTrip(t *testing.T) {
	in := Meta{
		Slug: "pokies", Name: "Pokies", ShortDescription: "spin to win",
		MinPlayers: 1, MaxPlayers: 5,
		ConfigSpecs: []ConfigSpec{
			{Key: "odds-variant", Title: "Odds variant", Description: "PAR sheet: weights + paytable.",
				Type: ConfigJSON, Default: `{"name":"Default"}`, Schema: `{"type":"object"}`},
			{Key: "motd", Title: "Banner", Description: "Floor banner text.", Type: ConfigText},
			{Key: "speed", Title: "Speed", Description: "Reel speed.", Type: ConfigNumber, Default: "1"},
			{Key: "wild", Title: "Wilds", Description: "Enable wild faces.", Type: ConfigBool, Default: "false"},
		},
	}
	out, err := DecodeMeta(EncodeMeta(in))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

// TestMetaPreConfigBytesDecode pins the presence guard: a payload encoded
// WITHOUT the trailing config-spec section (a pre-v2.3 meta) decodes cleanly
// with no specs. The bytes are hand-built to the old layout — EncodeMeta can
// no longer produce them.
func TestMetaPreConfigBytesDecode(t *testing.T) {
	var w Buf
	w.Str("pokies")
	w.Str("Pokies")
	w.Str("spin to win")
	w.U16(1)
	w.U16(5)
	w.U16(1)
	w.Str("slots")
	w.Str("")
	w.Str("")
	w.Str("")
	w.Bool(true) // leaderboard block ends the old payload
	w.Str("Credits")
	w.U8(0)
	w.U8(0)
	w.U8(0)
	out, err := DecodeMeta(w.B)
	if err != nil {
		t.Fatal(err)
	}
	if out.ConfigSpecs != nil {
		t.Fatalf("pre-config bytes decoded specs: %+v", out.ConfigSpecs)
	}
	if out.Slug != "pokies" || !out.HasLeaderboard || out.MetricLabel != "Credits" {
		t.Fatalf("pre-config fields corrupted: %+v", out)
	}
}

func TestMetaZeroCountConfigSection(t *testing.T) {
	b := EncodeMeta(Meta{Slug: "x", Name: "y"})
	// The new encoder always writes the section: the payload ends with a
	// zero u16 count, and it decodes to nil specs.
	if b[len(b)-2] != 0 || b[len(b)-1] != 0 {
		t.Fatalf("want trailing zero count, got % x", b[len(b)-2:])
	}
	out, err := DecodeMeta(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.ConfigSpecs != nil {
		t.Fatalf("zero-count section decoded specs: %+v", out.ConfigSpecs)
	}
}

func TestValidateConfigSpecs(t *testing.T) {
	ok := []ConfigSpec{
		{Key: "odds-variant", Type: ConfigJSON, Schema: `{"type":"object"}`},
		{Key: "motd", Type: ConfigText},
	}
	if err := ValidateConfigSpecs(ok); err != nil {
		t.Fatalf("valid specs rejected: %v", err)
	}
	if err := ValidateConfigSpecs(nil); err != nil {
		t.Fatalf("nil specs rejected: %v", err)
	}
	cases := map[string][]ConfigSpec{
		"empty key":          {{Key: "", Type: ConfigText}},
		"duplicate key":      {{Key: "k", Type: ConfigText}, {Key: "k", Type: ConfigBool}},
		"reserved prefix":    {{Key: "host.heartbeat_ms", Type: ConfigNumber}},
		"unknown type":       {{Key: "k", Type: 9}},
		"schema on non-json": {{Key: "k", Type: ConfigNumber, Schema: `{}`}},
		"schema not JSON":    {{Key: "k", Type: ConfigJSON, Schema: `{nope`}},
	}
	for name, specs := range cases {
		if err := ValidateConfigSpecs(specs); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestMetaRejectsEmptySlug(t *testing.T) {
	if _, err := DecodeMeta(EncodeMeta(Meta{Name: "x"})); err == nil {
		t.Fatal("want error for empty slug")
	}
}

func TestCellRoundTrip(t *testing.T) {
	buf := make([]byte, FrameBytes)
	in := Cell{Rune: '╭', Cp2: 0xFE0F, Cp3: 0x20E3, FGSet: true, FGR: 1, FGG: 2, FGB: 3, BGSet: true, BGR: 4, BGG: 5, BGB: 6, Attr: 0b1010, Cont: true}
	PutCell(buf, 1234, in)
	if out := GetCell(buf, 1234); out != in {
		t.Fatalf("cell mismatch: in=%+v out=%+v", in, out)
	}
	// Canonical-zero: pad bytes @22..23 are always zero.
	o := 1234 * CellBytes
	if buf[o+22] != 0 || buf[o+23] != 0 {
		t.Fatalf("pad not canonical zero: %d %d", buf[o+22], buf[o+23])
	}
	// neighbors untouched
	if out := GetCell(buf, 1233); out != (Cell{}) {
		t.Fatalf("neighbor dirtied: %+v", out)
	}
	if err := CheckFrame(buf); err != nil {
		t.Fatal(err)
	}
	if err := CheckFrame(buf[:100]); err == nil {
		t.Fatal("want error for short frame")
	}
}

func TestResultRoundTrip(t *testing.T) {
	in := Result{Rankings: []Ranking{
		{PlayerIdx: 0, Metric: 9000, Rank: 1, Status: StatusFinished},
		{PlayerIdx: 2, Metric: -5, Rank: 2, Status: StatusDNF},
	}}
	out, err := DecodeResult(EncodeResult(in))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func FuzzDecodeMeta(f *testing.F) {
	f.Add(EncodeMeta(Meta{Slug: "x", Name: "y"}))
	f.Add(EncodeMeta(Meta{Slug: "x", Name: "y", ConfigSpecs: []ConfigSpec{
		{Key: "k", Title: "K", Type: ConfigJSON, Default: "{}", Schema: `{"type":"object"}`},
	}}))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodeMeta(b) // must never panic
	})
}

func FuzzDecodeCtx(f *testing.F) {
	var w Buf
	EncodeCtx(&w, Ctx{Members: []Player{{Handle: "h"}}})
	f.Add(w.B)
	f.Add(bytes.Repeat([]byte{0xff}, 64))
	f.Fuzz(func(t *testing.T, b []byte) {
		r := &Rd{B: b}
		_ = DecodeCtx(r) // must never panic
	})
}

func FuzzDecodeResult(f *testing.F) {
	f.Add(EncodeResult(Result{Rankings: []Ranking{{Metric: 1}}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodeResult(b) // must never panic
	})
}
