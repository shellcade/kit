package wire

import "testing"

// The game-kind trailer: round trip, presence-guarded absence, validation.
func TestMetaGameKindRoundTrip(t *testing.T) {
	m := Meta{Slug: "casino", Name: "C", MinPlayers: 1, MaxPlayers: 4,
		GameKind: GameKindCasino, MaxPayoutMultiplier: 10000}
	got, err := DecodeMeta(EncodeMeta(m))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.GameKind != GameKindCasino || got.MaxPayoutMultiplier != 10000 {
		t.Fatalf("round trip = kind %d mult %d, want casino/10000", got.GameKind, got.MaxPayoutMultiplier)
	}

	// A payload ending before the section (a pre-revision-7 artifact) decodes
	// as GameKindGame with no multiplier.
	b := EncodeMeta(m)
	old, err := DecodeMeta(b[:len(b)-5])
	if err != nil {
		t.Fatalf("pre-kind decode: %v", err)
	}
	if old.GameKind != GameKindGame || old.MaxPayoutMultiplier != 0 {
		t.Fatalf("pre-kind payload = kind %d mult %d, want game/0", old.GameKind, old.MaxPayoutMultiplier)
	}
}

func TestValidateGameKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind uint8
		mult uint32
		ok   bool
	}{
		{"game", GameKindGame, 0, true},
		{"game with multiplier", GameKindGame, 5, false},
		{"casino", GameKindCasino, 500, true},
		{"casino without multiplier", GameKindCasino, 0, false},
		{"unknown kind", 9, 0, false},
	} {
		err := ValidateGameKind(tc.kind, tc.mult)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
