package wire

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Cross-language golden vectors for the per-member ctx character section
// (CtxFeatCharacter), following the scalars.txt discipline exactly: this
// package is the EMITTER and the freshness gate, the Rust SDK replays the
// committed file. Direction-aware like the ctx_* scalar vectors — a Ctx is
// host-encoded, flowing host→guest only, so the Rust side DECODES these
// payloads (asserting every field and the reader position at the trailing
// event-extra) and never encodes a Ctx of its own.
//
// ctxCharacterGoldenPath lives under the Rust crate so its replay test is
// self-contained (include_str!); TestCtxCharacterGoldenFresh regenerates the
// content on every plain `go test` run and fails if the committed file has
// gone stale against the current encoders.
const ctxCharacterGoldenPath = "../rust/tests/golden/ctx_character.txt"

// ctxCharacterFixture is the canonical character roster, mirrored verbatim by
// the kittest conformance fixture (kittest/character_conformance_test.go) and
// by the Rust replay fixture: two members, one of them a guest, with every
// character field non-zero somewhere across the pair.
func ctxCharacterFixture() Ctx {
	return Ctx{
		NowUnixNanos: 9,
		Seed:         7,
		SeedSet:      true,
		Mode:         ModePrivate,
		Capacity:     4,
		MinPlayers:   2,
		Settled:      true,
		Members: []Player{
			{Handle: "ana", AccountID: "a-1", Conn: "c-1", Kind: KindMember,
				Character: Character{Glyph: "λ", InkR: 0x39, InkG: 0xFF, InkB: 0x14, BgR: 0x2D, BgG: 0x1B, BgB: 0x4E, Fallback: 'L'}},
			{Handle: "bob", AccountID: "a-2", Conn: "c-2", Kind: KindGuest,
				Character: Character{Glyph: "@", InkR: 1, InkG: 2, InkB: 3, BgR: 4, BgG: 5, BgB: 6, Fallback: '@'}},
		},
	}
}

// ctxCharacterVectors emits both member-bearing forms with the character
// section, each followed by the u32 event-extra (the same stand-in for the
// per-export trailing args the scalar ctx vectors carry — decoding must leave
// the reader exactly there).
func ctxCharacterVectors() []scalarVector {
	fix := ctxCharacterFixture()
	ctx := func(encode func(w *Buf)) []byte {
		var w Buf
		encode(&w)
		w.U32(ctxEventExtra)
		return w.B
	}
	return []scalarVector{
		{"ctx_character_legacy", ctx(func(w *Buf) {
			EncodeCtxFeat(w, fix, CtxFeatCharacter)
		})},
		{"ctx_character_epoch_full", ctx(func(w *Buf) {
			EncodeCtxEpochFeat(w, fix, 42, true, CtxFeatCharacter|CtxFeatRosterEpoch)
		})},
	}
}

const ctxCharacterGoldenHeader = `# Cross-language golden vectors for the per-member ctx character section
# (CtxFeatCharacter, ABI.md §4.1), emitted by the Go reference encoders in
# kit/wire (character_golden_test.go) and decoded by the Rust SDK. A Ctx is
# host-encoded (host→guest only), so the Rust side asserts DECODE parity —
# every field plus the reader position — and never encodes a Ctx of its own.
#
# DO NOT EDIT BY HAND. When the encoding legitimately changes, review the
# wire-visible change, then regenerate and commit:
#
#   WIRE_SCALAR_GOLDEN_WRITE=1 go test -run TestCtxCharacterGoldenFresh ./wire/
#
# Format: one "name = hex" line per vector (the scalars.txt shape). Both
# vectors carry the canonical two-member character roster (member "ana" with
# glyph λ, guest "bob" with glyph @) and end with a trailing u32 event-extra
# (7) standing in for the per-export trailing args.
#
#   ctx_character_legacy      EncodeCtxFeat — legacy full-roster form,
#                             features = CtxFeatCharacter
#   ctx_character_epoch_full  EncodeCtxEpochFeat — 0xFFFE full-at-epoch form,
#                             epoch 42, features = CtxFeatCharacter|CtxFeatRosterEpoch
`

func renderCtxCharacterGolden() string {
	var b strings.Builder
	b.WriteString(ctxCharacterGoldenHeader)
	for _, v := range ctxCharacterVectors() {
		fmt.Fprintf(&b, "%s = %x\n", v.name, v.payload)
	}
	return b.String()
}

// TestCtxCharacterGoldenFresh is the freshness gate, identical in shape to
// TestScalarGoldenFresh: the committed vector file must equal what the
// CURRENT encoders emit. Set WIRE_SCALAR_GOLDEN_WRITE=1 to regenerate after a
// reviewed encoding change.
func TestCtxCharacterGoldenFresh(t *testing.T) {
	want := renderCtxCharacterGolden()
	if os.Getenv("WIRE_SCALAR_GOLDEN_WRITE") != "" {
		if err := os.WriteFile(ctxCharacterGoldenPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", ctxCharacterGoldenPath, len(want))
		return
	}
	got, err := os.ReadFile(ctxCharacterGoldenPath)
	if err != nil {
		t.Fatalf("reading committed ctx character golden vectors: %v\nregenerate with:\n  WIRE_SCALAR_GOLDEN_WRITE=1 go test -run TestCtxCharacterGoldenFresh ./wire/", err)
	}
	if string(got) != want {
		t.Fatalf("%s is STALE against the current Go encoders.\n"+
			"An encoding's byte output changed — review the change (it is wire-visible\n"+
			"and may need a wire.Revision bump and an ABI.md entry), then regenerate,\n"+
			"commit, and make sure the Rust replay fixtures still describe the same\n"+
			"logical payloads:\n  WIRE_SCALAR_GOLDEN_WRITE=1 go test -run TestCtxCharacterGoldenFresh ./wire/",
			ctxCharacterGoldenPath)
	}
}

// TestCtxCharacterGoldenDecode pins DecodeCtxFeat over the emitted vectors,
// including the reader position at the trailing event-extra — the Go twin of
// the Rust replay assertions.
func TestCtxCharacterGoldenDecode(t *testing.T) {
	vectors := make(map[string][]byte, len(ctxCharacterVectors()))
	for _, v := range ctxCharacterVectors() {
		vectors[v.name] = v.payload
	}
	base := ctxCharacterFixture()
	cases := []struct {
		name string
		want Ctx
	}{
		{"ctx_character_legacy", base},
		{"ctx_character_epoch_full", func() Ctx {
			c := base
			c.RosterEpoch, c.RosterEpochSet = 42, true
			return c
		}()},
	}
	for _, tc := range cases {
		r := &Rd{B: vectors[tc.name]}
		got := DecodeCtxFeat(r, CtxFeatCharacter)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got  %+v\n want %+v", tc.name, got, tc.want)
		}
		if extra := r.U32(); extra != ctxEventExtra {
			t.Errorf("%s: reader not at the event extras: got u32 %d, want %d", tc.name, extra, ctxEventExtra)
		}
		if err := r.Err(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if r.Off != len(r.B) {
			t.Errorf("%s: %d bytes left after the event extras", tc.name, len(r.B)-r.Off)
		}
	}
}
