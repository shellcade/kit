package gameabi

// Host-side conformance for the add-character-builder change: the per-guest
// CtxFeatCharacter declaration (meta.CtxFeatures) selects whether encodeCtx /
// encodeCtxEpoch emit per-member character sections, and a non-declaring
// guest's bytes are identical to the pre-feature encoding (the v2.8 guarantee
// at host level).

import (
	"bytes"
	"testing"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/sdk"
)

func charRoster() []sdk.Player {
	return []sdk.Player{
		{AccountID: "a1", Handle: "ada", Kind: sdk.KindMember, Conn: "c1",
			Character: sdk.Character{Glyph: "@", InkR: 1, InkG: 2, InkB: 3, BgR: 4, BgG: 5, BgB: 6, Fallback: '@'}},
		{AccountID: "", Handle: "guest", Kind: sdk.KindGuest, Conn: "c2",
			Character: sdk.Character{Glyph: "ż", InkR: 250, InkG: 0, InkB: 128, BgR: 9, BgG: 10, BgB: 11, Fallback: 'z'}},
	}
}

func charCfg() sdk.RoomConfig {
	return sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 4, MinPlayers: 2, Seed: 7, SeedSet: true}
}

// wantWireChar is the wire shape of charRoster()[i].Character.
func wantWireChar(p sdk.Player) wire.Character {
	return wire.Character{
		Glyph: p.Character.Glyph,
		InkR:  p.Character.InkR, InkG: p.Character.InkG, InkB: p.Character.InkB,
		BgR: p.Character.BgR, BgG: p.Character.BgG, BgB: p.Character.BgB,
		Fallback: p.Character.Fallback,
	}
}

// A guest whose meta declares CtxFeatCharacter receives a character section
// for every roster member, intact through a wire decode, in BOTH the legacy
// form and the roster-epoch full form.
func TestEncodeCtxCharacterSections(t *testing.T) {
	roster := charRoster()

	// Legacy form (meta declares CtxFeatCharacter only).
	w := encodeCtx(99, charCfg(), roster, false, wire.CtxFeatCharacter)
	got := wire.DecodeCtxFeat(&wire.Rd{B: w.B}, wire.CtxFeatCharacter)
	if len(got.Members) != 2 {
		t.Fatalf("legacy form: decoded %d members, want 2", len(got.Members))
	}
	for i, p := range roster {
		if got.Members[i].Character != wantWireChar(p) {
			t.Fatalf("legacy form member %d character:\n got=%+v\nwant=%+v", i, got.Members[i].Character, wantWireChar(p))
		}
	}

	// Roster-epoch full form (meta declares both features).
	feats := wire.CtxFeatCharacter | wire.CtxFeatRosterEpoch
	w = encodeCtxEpoch(99, charCfg(), roster, false, 3, true, feats)
	got = wire.DecodeCtxFeat(&wire.Rd{B: w.B}, feats)
	if !got.RosterEpochSet || got.RosterEpoch != 3 || got.RosterUnchanged {
		t.Fatalf("epoch-full form: epoch state %+v", got)
	}
	if len(got.Members) != 2 {
		t.Fatalf("epoch-full form: decoded %d members, want 2", len(got.Members))
	}
	for i, p := range roster {
		if got.Members[i].Character != wantWireChar(p) {
			t.Fatalf("epoch-full member %d character:\n got=%+v\nwant=%+v", i, got.Members[i].Character, wantWireChar(p))
		}
	}
}

// A non-declaring guest (meta features 0) gets byte-identical encodings to the
// same roster with zero-value characters — populating sdk.Player.Character on
// the host never changes what a pre-character guest receives.
func TestEncodeCtxNonDeclaringBytesUnchanged(t *testing.T) {
	withChars := charRoster()
	zeroed := charRoster()
	for i := range zeroed {
		zeroed[i].Character = sdk.Character{}
	}

	a := encodeCtx(99, charCfg(), withChars, true, 0)
	b := encodeCtx(99, charCfg(), zeroed, true, 0)
	if !bytes.Equal(a.B, b.B) {
		t.Fatalf("legacy form: features=0 encoding depends on character values:\n with=%x\n zero=%x", a.B, b.B)
	}

	// Epoch full form, roster-epoch declared but NOT the character bit.
	a = encodeCtxEpoch(99, charCfg(), withChars, true, 5, true, wire.CtxFeatRosterEpoch)
	b = encodeCtxEpoch(99, charCfg(), zeroed, true, 5, true, wire.CtxFeatRosterEpoch)
	if !bytes.Equal(a.B, b.B) {
		t.Fatalf("epoch-full form: roster-epoch-only encoding depends on character values:\n with=%x\n zero=%x", a.B, b.B)
	}

	// And the unknown-bit mask: a meta declaring bits this host's wire revision
	// does not define encodes as if only the known bits were set (decodeMeta's
	// tolerance posture, applied at encode time).
	a = encodeCtx(99, charCfg(), withChars, true, uint32(1<<30))
	b = encodeCtx(99, charCfg(), withChars, true, 0)
	if !bytes.Equal(a.B, b.B) {
		t.Fatal("unknown feature bits leaked into the encoding")
	}
}
