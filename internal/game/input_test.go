package game

import (
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

func TestKnownInputTolerance(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"rune", Input{Kind: InputRune, Rune: 'x'}, true},
		{"known key", Input{Kind: InputKey, Key: KeyEnter}, true},
		{"last known key", Input{Kind: InputKey, Key: KeyCtrlC}, true},
		{"unknown key", Input{Kind: InputKey, Key: KeyCtrlC + 1}, false},
		{"far unknown key", Input{Kind: InputKey, Key: 200}, false},
		{"unknown kind", Input{Kind: 99}, false},
	}
	for _, c := range cases {
		if got := knownInput(c.in); got != c.want {
			t.Errorf("%s: knownInput = %v, want %v", c.name, got, c.want)
		}
	}
}

// The input payload decoder (as used by ExportInput: kind u8, rune u32, key u8)
// must tolerate trailing bytes beyond the fields it knows — a future minor that
// appends mouse coordinates or modifiers must not break a v2 guest.
func TestInputDecoderToleratesTrailingBytes(t *testing.T) {
	// Encode a known input followed by the player index prefix ExportInput reads,
	// plus extra trailing bytes the v2 guest does not understand.
	var w wire.Buf
	w.U32(0)                                  // playerIdx (decodePlayer reads this first)
	w.U8(uint8(InputKey))                     // kind
	w.U32(0)                                  // rune
	w.U8(uint8(KeyUp))                        // key
	w.B = append(w.B, 0xDE, 0xAD, 0xBE, 0xEF) // trailing future fields

	r := &wire.Rd{B: w.B}
	idx := int(r.U32())
	if r.Bad || idx != 0 {
		t.Fatalf("player idx decode failed: idx=%d bad=%v", idx, r.Bad)
	}
	var in Input
	in.Kind = InputKind(r.U8())
	in.Rune = rune(r.U32())
	in.Key = Key(r.U8())
	if r.Bad {
		t.Fatal("decode of known input fields hit a short read")
	}
	if in.Kind != InputKey || in.Key != KeyUp {
		t.Fatalf("decoded wrong input: %+v", in)
	}
	if !knownInput(in) {
		t.Fatal("a known input with trailing bytes was treated as unknown")
	}
	// The trailing bytes are simply left unread — no error.
	if err := r.Err(); err != nil {
		t.Fatalf("trailing bytes must not error: %v", err)
	}
}
