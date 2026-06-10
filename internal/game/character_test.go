package game

import (
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

func featCtxPayload(features uint32, members ...wire.Player) []byte {
	var w wire.Buf
	wire.EncodeCtxFeat(&w, wire.Ctx{
		NowUnixNanos: 1, Seed: 7, SeedSet: true, Capacity: 1000, MinPlayers: 1,
		Members: members,
	}, features)
	return w.B
}

func epochFeatCtxPayload(features, epoch uint32, full bool, members ...wire.Player) []byte {
	var w wire.Buf
	wire.EncodeCtxEpochFeat(&w, wire.Ctx{
		NowUnixNanos: 1, Seed: 7, SeedSet: true, Capacity: 1000, MinPlayers: 1,
		Members: members,
	}, epoch, full, features)
	return w.B
}

// Legacy-form decode with CtxFeatCharacter declared: characters decode per
// member, the byte-skim cache stays aligned across the character section
// (identical bytes ⇒ changed=false), and a character-only change re-decodes.
func TestDecodeCtxCharacterSections(t *testing.T) {
	resetRosterState()
	declaredCtxFeatures = wire.CtxFeatCharacter
	defer func() { declaredCtxFeatures = 0 }()

	ada := wire.Player{Handle: "ada", AccountID: "a", Conn: "c1", Kind: 1,
		Character: wire.Character{Glyph: "λ", InkR: 10, InkG: 20, InkB: 30, BgR: 1, BgG: 2, BgB: 3, Fallback: 'L'}}
	bob := wire.Player{Handle: "bob", AccountID: "b", Conn: "c2", Kind: 1,
		Character: wire.Character{Glyph: "@", InkR: 200, InkG: 100, InkB: 50, Fallback: '@'}}

	c1, _, changed := decodeCtx(featCtxPayload(wire.CtxFeatCharacter, ada, bob))
	if !changed || len(c1.members) != 2 {
		t.Fatalf("first decode: changed=%v members=%d", changed, len(c1.members))
	}
	want0 := Character{Glyph: "λ", InkR: 10, InkG: 20, InkB: 30, BgR: 1, BgG: 2, BgB: 3, Fallback: 'L'}
	want1 := Character{Glyph: "@", InkR: 200, InkG: 100, InkB: 50, Fallback: '@'}
	if c1.members[0].Character != want0 || c1.members[1].Character != want1 {
		t.Fatalf("characters = %+v / %+v", c1.members[0].Character, c1.members[1].Character)
	}

	// Same bytes again: the skim must skip the character section exactly so
	// the memcmp region matches and the cached roster is reused.
	c2, _, changed := decodeCtx(featCtxPayload(wire.CtxFeatCharacter, ada, bob))
	if changed {
		t.Fatal("identical payload re-decoded (skim misaligned over character section)")
	}
	if c2.members[0].Character != want0 || c2.members[1].Character != want1 {
		t.Fatalf("cached characters lost: %+v / %+v", c2.members[0].Character, c2.members[1].Character)
	}

	// A character-only change must register as a roster change and decode the
	// new value (the characters live inside the compared region).
	bob.Character.InkR = 201
	c3, _, changed := decodeCtx(featCtxPayload(wire.CtxFeatCharacter, ada, bob))
	if !changed {
		t.Fatal("InkR flip not detected as a change")
	}
	if c3.members[1].Character.InkR != 201 {
		t.Fatalf("new InkR not decoded: %+v", c3.members[1].Character)
	}
}

// Roster-epoch full form with both feature bits: characters decode, the epoch
// is honoured, and the unchanged sentinel reuses the cached roster WITH the
// characters intact.
func TestDecodeCtxCharacterEpochForm(t *testing.T) {
	resetRosterState()
	declaredCtxFeatures = wire.CtxFeatCharacter | wire.CtxFeatRosterEpoch
	defer func() { declaredCtxFeatures = 0 }()

	ada := wire.Player{Handle: "ada", AccountID: "a", Conn: "c1", Kind: 1,
		Character: wire.Character{Glyph: "λ", InkR: 9, Fallback: 'L'}}
	feats := wire.CtxFeatCharacter | wire.CtxFeatRosterEpoch

	c1, _, changed := decodeCtx(epochFeatCtxPayload(feats, 7, true, ada))
	if !changed || len(c1.members) != 1 {
		t.Fatalf("full form: changed=%v members=%d", changed, len(c1.members))
	}
	want := Character{Glyph: "λ", InkR: 9, Fallback: 'L'}
	if c1.members[0].Character != want {
		t.Fatalf("character = %+v", c1.members[0].Character)
	}
	if !rosterCacheEpochSet || rosterCacheEpoch != 7 {
		t.Fatalf("cache epoch = %d set=%v", rosterCacheEpoch, rosterCacheEpochSet)
	}

	c2, _, changed := decodeCtx(epochFeatCtxPayload(feats, 7, false))
	if changed || len(c2.members) != 1 {
		t.Fatalf("unchanged form: changed=%v members=%d", changed, len(c2.members))
	}
	if c2.members[0].Character != want {
		t.Fatalf("cached character lost on unchanged sentinel: %+v", c2.members[0].Character)
	}
}

// Feature undeclared: legacy bytes decode exactly as before and every
// member's Character is the zero value.
func TestDecodeCtxNoCharacterFeature(t *testing.T) {
	resetRosterState()

	ada := wire.Player{Handle: "ada", AccountID: "a", Conn: "c1", Kind: 1}
	bob := wire.Player{Handle: "bob", AccountID: "b", Conn: "c2", Kind: 0}
	var w wire.Buf
	wire.EncodeCtx(&w, wire.Ctx{
		NowUnixNanos: 1, Seed: 7, SeedSet: true, Capacity: 8, MinPlayers: 1,
		Members: []wire.Player{ada, bob},
	})

	c, _, changed := decodeCtx(w.B)
	if !changed || len(c.members) != 2 {
		t.Fatalf("decode: changed=%v members=%d", changed, len(c.members))
	}
	for i, m := range c.members {
		if m.Character != (Character{}) {
			t.Fatalf("member %d Character = %+v, want zero value", i, m.Character)
		}
	}
}
