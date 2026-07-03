package gameabi

import (
	"reflect"
	"testing"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/sdk"
)

// TestDecodeMetaControls pins the wire → sdk mapping of declared controls,
// and that a pre-controls payload decodes with nil Controls.
func TestDecodeMetaControls(t *testing.T) {
	b := wire.EncodeMeta(wire.Meta{
		Slug: "chess", Name: "Chess", MinPlayers: 2, MaxPlayers: 2,
		Controls: []wire.ControlDecl{
			{Kind: wire.InputRune, Rune: 'r', Label: "RESIGN"},
			{Kind: wire.InputKey, Key: wire.KeyCodeBackspace, Label: "UNDO"},
		},
	})
	m, err := decodeMeta(b)
	if err != nil {
		t.Fatal(err)
	}
	want := []sdk.ControlDecl{
		{Kind: sdk.InputRune, Rune: 'r', Label: "RESIGN"},
		{Kind: sdk.InputKey, Key: sdk.KeyBackspace, Label: "UNDO"},
	}
	if !reflect.DeepEqual(m.Controls, want) {
		t.Fatalf("controls mismatch:\n got=%+v\nwant=%+v", m.Controls, want)
	}

	// Pre-controls payload (no trailing section): nil Controls, no error.
	pre := wire.EncodeMeta(wire.Meta{Slug: "old", Name: "Old", MinPlayers: 1, MaxPlayers: 2})
	pre = pre[:len(pre)-7] // strip the game-kind section + zero-count controls section
	m, err = decodeMeta(pre)
	if err != nil {
		t.Fatal(err)
	}
	if m.Controls != nil {
		t.Fatalf("pre-controls payload decoded controls: %+v", m.Controls)
	}
}

// TestDecodeMetaRefusesInvalidControls pins the malformed-artifact posture:
// declarations a kit SDK could never encode are refused at load.
func TestDecodeMetaRefusesInvalidControls(t *testing.T) {
	b := wire.EncodeMeta(wire.Meta{
		Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 1,
		Controls: []wire.ControlDecl{{Kind: wire.InputRune, Rune: 'r', Label: ""}},
	})
	if _, err := decodeMeta(b); err == nil {
		t.Fatal("empty-label control decl decoded without error")
	}
}
