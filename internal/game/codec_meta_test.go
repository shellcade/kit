package game

import (
	"reflect"
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

// TestEncodeMetaMapsConfigSpecs pins the authoring → wire mapping for declared
// config key specs, field by field and in declaration order.
func TestEncodeMetaMapsConfigSpecs(t *testing.T) {
	m := GameMeta{
		Slug: "pokies", Name: "Pokies", MinPlayers: 1, MaxPlayers: 5,
		Config: []ConfigKeySpec{
			{Key: "odds-variant", Title: "Odds variant", Description: "PAR sheet.",
				Type: ConfigJSON, Default: `{"name":"Default"}`, Schema: `{"type":"object"}`},
			{Key: "motd", Title: "Banner", Description: "Floor banner.", Type: ConfigText},
		},
	}
	out, err := wire.DecodeMeta(encodeMeta(m))
	if err != nil {
		t.Fatal(err)
	}
	want := []wire.ConfigSpec{
		{Key: "odds-variant", Title: "Odds variant", Description: "PAR sheet.",
			Type: wire.ConfigJSON, Default: `{"name":"Default"}`, Schema: `{"type":"object"}`},
		{Key: "motd", Title: "Banner", Description: "Floor banner.", Type: wire.ConfigText},
	}
	if !reflect.DeepEqual(out.ConfigSpecs, want) {
		t.Fatalf("config specs mismatch:\n got=%+v\nwant=%+v", out.ConfigSpecs, want)
	}
}

// TestEncodeMetaPanicsOnInvalidConfig pins the fail-fast posture: an invalid
// declaration is an authoring bug that surfaces at meta() time.
func TestEncodeMetaPanicsOnInvalidConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want panic for a host.-prefixed config spec key")
		}
	}()
	encodeMeta(GameMeta{
		Slug: "x", Name: "X", MinPlayers: 1, MaxPlayers: 1,
		Config: []ConfigKeySpec{{Key: "host.heartbeat_ms", Type: ConfigNumber}},
	})
}

// TestEncodeMetaStampsWireRevision pins that the SDK stamps the compiled-in
// wire revision into every meta — it is not author-settable, so every
// artifact built with this kit declares the contract revision it may assume
// (the host's deploy-order anchor, ABI.md §4.2 / §5).
func TestEncodeMetaStampsWireRevision(t *testing.T) {
	out, err := wire.DecodeMeta(encodeMeta(GameMeta{Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if out.WireRevision != wire.Revision {
		t.Fatalf("meta declares wire revision %d, want wire.Revision = %d", out.WireRevision, wire.Revision)
	}
}
