package kittest_test

// Public ABI-v2 conformance for the per-member ctx character section
// (CtxFeatCharacter, ABI.md §4.1/§4.2), in the same spirit as
// conformance_test.go: only the exported kit/wire/kittest API.
//
// Layer note: the kittest double drives the NATIVE authoring path (callbacks
// with already-built kit.Player values) and never encodes or decodes a Ctx —
// the wasm codec that does is internal. So the ctx-section coverage here
// drives the public `wire` package directly, exactly as the host encodes and
// a from-scratch guest decodes (wire.EncodeCtxFeat / wire.DecodeCtxFeat are
// both exported for that purpose), and then hands the decoded characters to a
// fixture game through kittest to cover the authoring half
// (Player.Character → kit.CharacterCell → a rendered cell).

import (
	"bytes"
	"testing"

	kit "github.com/shellcade/kit/v2"
	"github.com/shellcade/kit/v2/kittest"
	"github.com/shellcade/kit/v2/wire"
)

// ---- a fixture game that declares CtxFeatCharacter and draws the roster ------

type characterGame struct{}

func (characterGame) Meta() kit.GameMeta {
	return kit.GameMeta{
		Slug:        "conformance-character",
		Name:        "Conformance Character",
		MinPlayers:  1,
		MaxPlayers:  4,
		CtxFeatures: kit.CtxFeatCharacter,
	}
}
func (characterGame) NewRoom(kit.RoomConfig, kit.Services) kit.Handler { return &characterHandler{} }

type characterHandler struct{ kit.Base }

// OnJoin draws each member's character tile at row 0, one column per roster
// index — the zero-width-logic placement CharacterCell promises.
func (characterHandler) OnJoin(r kit.Room, _ kit.Player) {
	f := kit.NewFrame()
	for i, m := range r.Members() {
		f.Set(0, i, kit.CharacterCell(m.Character))
	}
	r.Identical(f)
}

// ---- the canonical character roster (mirrors wire's golden fixture) ----------

func characterCtx() wire.Ctx {
	return wire.Ctx{
		NowUnixNanos: 9,
		Seed:         7,
		SeedSet:      true,
		Mode:         wire.ModePrivate,
		Capacity:     4,
		MinPlayers:   2,
		Settled:      true,
		Members: []wire.Player{
			{Handle: "ana", AccountID: "a-1", Conn: "c-1", Kind: wire.KindMember,
				Character: wire.Character{Glyph: "λ", InkR: 0x39, InkG: 0xFF, InkB: 0x14, BgR: 0x2D, BgG: 0x1B, BgB: 0x4E, Fallback: 'L'}},
			{Handle: "bob", AccountID: "a-2", Conn: "c-2", Kind: wire.KindGuest,
				Character: wire.Character{Glyph: "@", InkR: 1, InkG: 2, InkB: 3, BgR: 4, BgG: 5, BgB: 6, Fallback: '@'}},
		},
	}
}

// ---- conformance: flag-on — characters decode and render ---------------------

func TestConformanceCharacterFlagOn(t *testing.T) {
	// The declaration itself must be a valid meta trailer (the rule set every
	// SDK applies at meta() encode time).
	if err := wire.ValidateMetaTrailer(characterGame{}.Meta().CtxFeatures, 0); err != nil {
		t.Fatalf("CtxFeatCharacter rejected as a meta trailer: %v", err)
	}

	want := characterCtx()

	// Both member-bearing forms: the legacy full roster and the full-at-epoch
	// sentinel form. Encode as the host does, decode as a declaring guest does.
	forms := []struct {
		name   string
		encode func(w *wire.Buf)
	}{
		{"legacy", func(w *wire.Buf) {
			wire.EncodeCtxFeat(w, want, wire.CtxFeatCharacter)
		}},
		{"epoch_full", func(w *wire.Buf) {
			wire.EncodeCtxEpochFeat(w, want, 42, true, wire.CtxFeatCharacter|wire.CtxFeatRosterEpoch)
		}},
	}

	var decoded []wire.Player
	for _, form := range forms {
		var w wire.Buf
		form.encode(&w)
		r := &wire.Rd{B: w.B}
		got := wire.DecodeCtxFeat(r, wire.CtxFeatCharacter)
		if err := r.Err(); err != nil {
			t.Fatalf("%s: decode: %v", form.name, err)
		}
		if r.Off != len(r.B) {
			t.Fatalf("%s: %d bytes left after decode", form.name, len(r.B)-r.Off)
		}
		if len(got.Members) != len(want.Members) {
			t.Fatalf("%s: %d members decoded, want %d", form.name, len(got.Members), len(want.Members))
		}
		for i, m := range got.Members {
			if m.Character != want.Members[i].Character {
				t.Fatalf("%s: member %d character = %+v, want %+v",
					form.name, i, m.Character, want.Members[i].Character)
			}
		}
		decoded = got.Members
	}

	// The authoring half: a room whose players carry the decoded characters;
	// the fixture game draws them via kit.CharacterCell.
	players := make([]kit.Player, len(decoded))
	for i, m := range decoded {
		players[i] = kit.Player{
			Handle: m.Handle, AccountID: m.AccountID, Conn: m.Conn, Kind: kit.Kind(m.Kind),
			Character: kit.Character{
				Glyph: m.Character.Glyph,
				InkR:  m.Character.InkR, InkG: m.Character.InkG, InkB: m.Character.InkB,
				BgR: m.Character.BgR, BgG: m.Character.BgG, BgB: m.Character.BgB,
				Fallback: m.Character.Fallback,
			},
		}
	}
	room := kittest.NewRoom(players...)
	h := characterGame{}.NewRoom(room.Config(), room.Services())
	h.OnStart(room)
	h.OnJoin(room, room.Players[0])

	f := room.LastFrame(room.Players[0])
	if f == nil {
		t.Fatal("no frame captured")
	}
	ana := f.Cells[0][0]
	if ana.Rune != 'λ' || ana.FG != kit.RGB(0x39, 0xFF, 0x14) || ana.BG != kit.RGB(0x2D, 0x1B, 0x4E) {
		t.Fatalf("ana's character cell wrong: %+v", ana)
	}
	bob := f.Cells[0][1]
	if bob.Rune != '@' || bob.FG != kit.RGB(1, 2, 3) || bob.BG != kit.RGB(4, 5, 6) {
		t.Fatalf("bob's character cell wrong: %+v", bob)
	}
}

// ---- conformance: flag-off — encodings are byte-identical to revision 4 ------

// TestConformanceCharacterFlagOff pins the v2.8 compatibility guarantee at the
// conformance level: for a guest that does NOT declare CtxFeatCharacter, a
// roster whose players carry characters encodes byte-identically to the same
// roster with zero-valued characters — the section simply does not exist on
// the wire, in any member-bearing form.
func TestConformanceCharacterFlagOff(t *testing.T) {
	withChars := characterCtx()
	zeroed := characterCtx()
	for i := range zeroed.Members {
		zeroed.Members[i].Character = wire.Character{}
	}

	encodings := []struct {
		name   string
		encode func(w *wire.Buf, c wire.Ctx)
	}{
		{"legacy", func(w *wire.Buf, c wire.Ctx) { wire.EncodeCtx(w, c) }},
		{"epoch_full", func(w *wire.Buf, c wire.Ctx) { wire.EncodeCtxEpoch(w, c, 42, true) }},
		{"epoch_unchanged", func(w *wire.Buf, c wire.Ctx) { wire.EncodeCtxEpoch(w, c, 42, false) }},
	}
	for _, enc := range encodings {
		var a, b wire.Buf
		enc.encode(&a, withChars)
		enc.encode(&b, zeroed)
		if !bytes.Equal(a.B, b.B) {
			t.Fatalf("%s: characters leaked into a features=0 encoding:\n with    %x\n zeroed  %x",
				enc.name, a.B, b.B)
		}
	}

	// And a non-declaring decode yields zero-valued characters.
	var w wire.Buf
	wire.EncodeCtx(&w, withChars)
	got := wire.DecodeCtx(&wire.Rd{B: w.B})
	for i, m := range got.Members {
		if m.Character != (wire.Character{}) {
			t.Fatalf("member %d Character = %+v, want zero value", i, m.Character)
		}
	}
}
