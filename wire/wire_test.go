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

func TestMetaRejectsEmptySlug(t *testing.T) {
	if _, err := DecodeMeta(EncodeMeta(Meta{Name: "x"})); err == nil {
		t.Fatal("want error for empty slug")
	}
}

func TestCellRoundTrip(t *testing.T) {
	buf := make([]byte, FrameBytes)
	in := Cell{Rune: '╭', FGSet: true, FGR: 1, FGG: 2, FGB: 3, BGSet: true, BGR: 4, BGG: 5, BGB: 6, Attr: 0b1010, Cont: true}
	PutCell(buf, 1234, in)
	if out := GetCell(buf, 1234); out != in {
		t.Fatalf("cell mismatch: in=%+v out=%+v", in, out)
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
