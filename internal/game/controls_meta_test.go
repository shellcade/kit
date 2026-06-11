package game

import (
	"reflect"
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

// TestEncodeMetaMapsControls pins the authoring → wire mapping for declared
// controls: RuneControl/KeyControl literals land as the wire kinds/values, in
// declaration order.
func TestEncodeMetaMapsControls(t *testing.T) {
	m := GameMeta{
		Slug: "chess", Name: "Chess", MinPlayers: 2, MaxPlayers: 2,
		Controls: []ControlDecl{
			RuneControl('r', "RESIGN"),
			KeyControl(KeyBackspace, "UNDO"),
		},
	}
	out, err := wire.DecodeMeta(encodeMeta(m))
	if err != nil {
		t.Fatal(err)
	}
	want := []wire.ControlDecl{
		{Kind: wire.InputRune, Rune: 'r', Label: "RESIGN"},
		{Kind: wire.InputKey, Key: wire.KeyCodeBackspace, Label: "UNDO"},
	}
	if !reflect.DeepEqual(out.Controls, want) {
		t.Fatalf("controls mismatch:\n got=%+v\nwant=%+v", out.Controls, want)
	}
}

// TestEncodeMetaPanicsOnInvalidControls pins the fail-fast posture: an
// invalid declaration panics at meta() encode time, not at load time.
func TestEncodeMetaPanicsOnInvalidControls(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("encodeMeta accepted an empty control label")
		}
	}()
	encodeMeta(GameMeta{
		Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 1,
		Controls: []ControlDecl{RuneControl('r', "")},
	})
}
