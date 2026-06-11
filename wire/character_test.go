package wire

import (
	"bytes"
	"testing"
)

func charPlayers() []Player {
	return []Player{
		// "λ" is a multi-byte single-code-point width-1 glyph: the str
		// length prefix counts UTF-8 bytes (2), not code points.
		{Handle: "ana", AccountID: "a-1", Conn: "c-1", Kind: KindMember,
			Character: Character{Glyph: "λ", InkR: 0x39, InkG: 0xFF, InkB: 0x14, BgR: 0x2D, BgG: 0x1B, BgB: 0x4E, Fallback: 'L'}},
		{Handle: "bob", AccountID: "a-2", Conn: "c-2", Kind: KindGuest,
			Character: Character{Glyph: "@", InkR: 1, InkG: 2, InkB: 3, BgR: 4, BgG: 5, BgB: 6, Fallback: '@'}},
	}
}

// Feature-off encoding must be byte-identical to the legacy encoder even when
// Character fields are populated (non-declaring guests see v2.8 bytes).
func TestCtxFeatureOffBytesUnchanged(t *testing.T) {
	c := Ctx{NowUnixNanos: 9, Seed: 7, SeedSet: true, Mode: 1, Capacity: 4, MinPlayers: 2,
		Members: charPlayers(), Settled: false}
	var legacy, feat0 Buf
	EncodeCtx(&legacy, c)
	EncodeCtxFeat(&feat0, c, 0)
	if !bytes.Equal(legacy.B, feat0.B) {
		t.Fatal("EncodeCtxFeat(features=0) is not byte-identical to EncodeCtx")
	}
	stripped := c
	stripped.Members = append([]Player(nil), c.Members...)
	for i := range stripped.Members {
		stripped.Members[i].Character = Character{}
	}
	var plain Buf
	EncodeCtx(&plain, stripped)
	if !bytes.Equal(legacy.B, plain.B) {
		t.Fatal("populated Character leaked into the feature-off encoding")
	}
}

// Round trip with the feature declared, in both member-bearing forms, plus the
// unchanged sentinel. Each encode appends a trailing event-extra byte and the
// decode must leave the reader exactly on it.
func TestCtxCharacterRoundTrip(t *testing.T) {
	c := Ctx{NowUnixNanos: 9, Seed: 7, SeedSet: true, Mode: 1, Capacity: 4, MinPlayers: 2,
		Members: charPlayers(), Settled: true}

	var w Buf
	EncodeCtxFeat(&w, c, CtxFeatCharacter)
	w.U8(0xAB) // event extra
	r := &Rd{B: w.B}
	got := DecodeCtxFeat(r, CtxFeatCharacter)
	if len(got.Members) != 2 || got.Members[0].Character != c.Members[0].Character ||
		got.Members[1].Character != c.Members[1].Character {
		t.Fatalf("legacy-form round trip lost character data: %+v", got.Members)
	}
	if !got.Settled {
		t.Fatalf("legacy-form round trip lost Settled: %+v", got)
	}
	if b := r.U8(); b != 0xAB || r.Bad {
		t.Fatalf("legacy-form event extras misaligned: got %#x bad=%v", b, r.Bad)
	}

	feats := CtxFeatCharacter | CtxFeatRosterEpoch
	var we Buf
	EncodeCtxEpochFeat(&we, c, 42, true, feats)
	we.U8(0xAB)
	re := &Rd{B: we.B}
	gote := DecodeCtxFeat(re, feats)
	if !gote.RosterEpochSet || gote.RosterEpoch != 42 {
		t.Fatalf("epoch lost: %+v", gote)
	}
	if len(gote.Members) != 2 || gote.Members[1].Character != c.Members[1].Character {
		t.Fatalf("epoch-form round trip lost character data: %+v", gote.Members)
	}
	if !gote.Settled {
		t.Fatalf("epoch-form round trip lost Settled: %+v", gote)
	}
	if b := re.U8(); b != 0xAB || re.Bad {
		t.Fatalf("epoch-form event extras misaligned: got %#x bad=%v", b, re.Bad)
	}

	var wu Buf
	EncodeCtxEpochFeat(&wu, c, 42, false, feats)
	wu.U8(0xAB)
	ru := &Rd{B: wu.B}
	gotu := DecodeCtxFeat(ru, feats)
	if !gotu.RosterUnchanged || len(gotu.Members) != 0 {
		t.Fatalf("unchanged-form wrong: %+v", gotu)
	}
	if !gotu.Settled {
		t.Fatalf("unchanged-form round trip lost Settled: %+v", gotu)
	}
	if b := ru.U8(); b != 0xAB || ru.Bad {
		t.Fatalf("unchanged-form event extras misaligned: got %#x bad=%v", b, ru.Bad)
	}
}

// A declaring decode of a feature-off payload must fail loudly (short read /
// Bad flag), never silently misparse — host-fault contract.
func TestCtxCharacterDecodeMismatchSetsBad(t *testing.T) {
	c := Ctx{Members: charPlayers()}
	var w Buf
	EncodeCtx(&w, c)
	r := &Rd{B: w.B}
	DecodeCtxFeat(r, CtxFeatCharacter)
	if r.Err() == nil {
		t.Fatal("mismatched decode did not set the error state")
	}
}

// The meta trailer accepts the new known bit and still rejects unknown bits.
func TestMetaTrailerAcceptsCharacterFeature(t *testing.T) {
	if err := ValidateMetaTrailer(CtxFeatCharacter, 0); err != nil {
		t.Fatalf("CtxFeatCharacter rejected by ValidateMetaTrailer: %v", err)
	}
	if err := ValidateMetaTrailer(CtxFeatCharacter|CtxFeatRosterEpoch, 100); err != nil {
		t.Fatalf("CtxFeatCharacter|CtxFeatRosterEpoch rejected: %v", err)
	}
	// Unknown bit must still be rejected.
	if err := ValidateMetaTrailer(1<<7, 0); err == nil {
		t.Fatal("unknown bit not rejected by ValidateMetaTrailer")
	}
}
