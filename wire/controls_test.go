package wire

import (
	"reflect"
	"strings"
	"testing"
)

func controlsMeta() Meta {
	return Meta{
		Slug:       "ctl",
		Name:       "Ctl",
		MinPlayers: 1,
		MaxPlayers: 2,
		Controls: []ControlDecl{
			{Kind: InputRune, Rune: 'r', Label: "RESIGN"},
			{Kind: InputRune, Rune: '✓', Label: "OK"}, // non-ASCII rune rides the u32
			{Kind: InputKey, Key: KeyCodeBackspace, Label: "UNDO"},
		},
	}
}

// Declared controls round-trip field-exact through the meta payload, for both
// rune and named-key inputs.
func TestMetaControlsRoundTrip(t *testing.T) {
	m := controlsMeta()
	got, err := DecodeMeta(EncodeMeta(m))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got.Controls, m.Controls) {
		t.Fatalf("controls round-trip mismatch:\n got %+v\nwant %+v", got.Controls, m.Controls)
	}
}

// A payload that ends after the wire-revision field (a pre-controls artifact)
// decodes as a valid meta with no declared controls.
func TestMetaPreControlsBytesDecode(t *testing.T) {
	m := controlsMeta()
	enc := EncodeMeta(m)
	// The controls section is the trailing 10+5+8+2 bytes here; rather than
	// hand-count, re-encode without controls and confirm it is a strict
	// prefix, then decode that prefix.
	m2 := m
	m2.Controls = nil
	pre := EncodeMeta(m2)
	// Drop the game-kind section (u8+u32) plus the empty controls u16 count.
	if !strings.HasPrefix(string(enc), string(pre[:len(pre)-7])) {
		t.Fatal("controls section is not a trailing addition")
	}
	got, err := DecodeMeta(pre[:len(pre)-7]) // drop even the empty u16 count
	if err != nil {
		t.Fatalf("decode pre-controls payload: %v", err)
	}
	if got.Controls != nil {
		t.Fatalf("pre-controls payload decoded controls: %+v", got.Controls)
	}
}

// An empty Controls list still writes the section (count 0) and decodes back
// to no declarations — the always-write discipline every trailer follows.
func TestMetaZeroCountControlsSection(t *testing.T) {
	m := controlsMeta()
	m.Controls = nil
	got, err := DecodeMeta(EncodeMeta(m))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Controls) != 0 {
		t.Fatalf("zero-count section decoded controls: %+v", got.Controls)
	}
}

// An unknown input kind cannot be skipped (the entry size depends on the
// kind), so it fails the decode rather than corrupting the framing.
func TestMetaUnknownControlKindFailsDecode(t *testing.T) {
	m := controlsMeta()
	m.Controls = []ControlDecl{{Kind: 9, Rune: 'x', Label: "X"}}
	if _, err := DecodeMeta(EncodeMeta(m)); err == nil {
		t.Fatal("unknown control kind decoded without error")
	}
}

func TestValidateControls(t *testing.T) {
	ok := []ControlDecl{
		{Kind: InputRune, Rune: 'r', Label: "RESIGN"},
		{Kind: InputKey, Key: KeyCodeBackspace, Label: "UNDO"},
		{Kind: InputKey, Key: KeyCodeCtrlC, Label: "KILL"},
	}
	if err := ValidateControls(ok); err != nil {
		t.Fatalf("valid decls rejected: %v", err)
	}
	if err := ValidateControls(nil); err != nil {
		t.Fatalf("nil decls rejected: %v", err)
	}

	bad := []struct {
		name  string
		decls []ControlDecl
	}{
		{"unknown kind", []ControlDecl{{Kind: 7, Rune: 'x', Label: "X"}}},
		{"non-printable rune", []ControlDecl{{Kind: InputRune, Rune: 0x1b, Label: "ESC"}}},
		{"key code zero", []ControlDecl{{Kind: InputKey, Key: 0, Label: "NONE"}}},
		{"key code past CtrlC", []ControlDecl{{Kind: InputKey, Key: 10, Label: "NEW"}}},
		{"empty label", []ControlDecl{{Kind: InputRune, Rune: 'r', Label: ""}}},
		{"label too long", []ControlDecl{{Kind: InputRune, Rune: 'r', Label: strings.Repeat("x", 17)}}},
		{"duplicate rune", []ControlDecl{
			{Kind: InputRune, Rune: 'r', Label: "A"},
			{Kind: InputRune, Rune: 'r', Label: "B"},
		}},
		{"duplicate key", []ControlDecl{
			{Kind: InputKey, Key: KeyCodeBackspace, Label: "A"},
			{Kind: InputKey, Key: KeyCodeBackspace, Label: "B"},
		}},
	}
	for _, tc := range bad {
		if err := ValidateControls(tc.decls); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}

	many := make([]ControlDecl, MaxControls+1)
	for i := range many {
		many[i] = ControlDecl{Kind: InputRune, Rune: rune('A' + i), Label: "K"}
	}
	if err := ValidateControls(many); err == nil {
		t.Error("over-cap list accepted")
	}
	if err := ValidateControls(many[:MaxControls]); err != nil {
		t.Errorf("at-cap list rejected: %v", err)
	}
}
