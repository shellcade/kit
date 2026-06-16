package gameabi

import (
	"testing"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/sdk"
)

// The host-side decoders sit on the trust boundary: every byte comes from an
// untrusted guest, so they must return errors — never panic — on arbitrary
// input. (The wire package itself is fuzzed in the kit repo; these targets
// cover the host mappings on top of it.)

func fuzzRoster() []sdk.Player {
	return []sdk.Player{
		{AccountID: "a1", Handle: "ada", Kind: sdk.KindMember, Conn: "c1"},
		{AccountID: "", Handle: "guest", Kind: sdk.KindGuest, Conn: "c2"},
	}
}

func FuzzDecodeMeta(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	valid := wire.EncodeMeta(wire.Meta{Slug: "fixture", Name: "Fixture", MinPlayers: 1, MaxPlayers: 2})
	f.Add(valid)
	f.Add(valid[:len(valid)/2]) // truncated valid prefix
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = decodeMeta(b) // must not panic
	})
}

// TestDecodeMetaConfigSpecs pins the wire → sdk mapping of declared config
// key specs, and that a pre-config payload (no trailing section) decodes with
// a nil Config.
func TestDecodeMetaConfigSpecs(t *testing.T) {
	b := wire.EncodeMeta(wire.Meta{
		Slug: "pokies", Name: "Pokies", MinPlayers: 1, MaxPlayers: 5,
		ConfigSpecs: []wire.ConfigSpec{
			{Key: "odds-variant", Title: "Odds variant", Description: "PAR sheet.",
				Type: wire.ConfigJSON, Default: `{"name":"Default"}`, Schema: `{"type":"object"}`},
			{Key: "motd", Title: "Banner", Type: wire.ConfigText},
		},
	})
	m, err := decodeMeta(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Config) != 2 {
		t.Fatalf("want 2 specs, got %+v", m.Config)
	}
	want := sdk.ConfigKeySpec{Key: "odds-variant", Title: "Odds variant", Description: "PAR sheet.",
		Type: sdk.ConfigJSON, Default: `{"name":"Default"}`, Schema: `{"type":"object"}`}
	if m.Config[0] != want {
		t.Fatalf("spec mismatch:\n got=%+v\nwant=%+v", m.Config[0], want)
	}
	if m.Config[1].Type != sdk.ConfigText {
		t.Fatalf("second spec type: %v", m.Config[1].Type)
	}

	// Hand-built pre-config payload: ends after the leaderboard byte.
	var w wire.Buf
	w.Str("old")
	w.Str("Old")
	w.Str("")
	w.U16(1)
	w.U16(1)
	w.U16(0) // tags
	w.Str("")
	w.Str("")
	w.Str("")
	w.Bool(false) // no leaderboard; payload ends here
	old, err := decodeMeta(w.B)
	if err != nil {
		t.Fatal(err)
	}
	if old.Config != nil {
		t.Fatalf("pre-config payload decoded specs: %+v", old.Config)
	}
}

// TestDecodeMetaRefusesInvalidConfigSpecs pins the load-time refusal: a
// hand-rolled artifact whose specs break the ABI authoring rules is malformed
// (kit SDKs cannot produce one — they fail at encode time).
func TestDecodeMetaRefusesInvalidConfigSpecs(t *testing.T) {
	for name, specs := range map[string][]wire.ConfigSpec{
		"reserved host. prefix": {{Key: "host.heartbeat_ms", Type: wire.ConfigNumber}},
		"duplicate keys":        {{Key: "k", Type: wire.ConfigText}, {Key: "k", Type: wire.ConfigText}},
		"schema on non-json":    {{Key: "k", Type: wire.ConfigNumber, Schema: "{}"}},
	} {
		b := wire.EncodeMeta(wire.Meta{Slug: "x", Name: "X", MinPlayers: 1, MaxPlayers: 1, ConfigSpecs: specs})
		if _, err := decodeMeta(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestDecodeMetaWireRevision (adopt kit v2.8.0) pins the wire → sdk mapping of
// the trailing wire-revision field: the declared value rides through verbatim
// (including one ahead of this host's wire.Revision — the catalog, not the
// codec, decides what to do with skew), and a pre-field payload decodes as
// the legacy 0 = unknown.
func TestDecodeMetaWireRevision(t *testing.T) {
	for _, rev := range []uint16{0, wire.Revision, wire.Revision + 1} {
		b := wire.EncodeMeta(wire.Meta{Slug: "x", Name: "X", MinPlayers: 1, MaxPlayers: 1, WireRevision: rev})
		m, err := decodeMeta(b)
		if err != nil {
			t.Fatalf("revision %d: %v", rev, err)
		}
		if m.WireRevision != rev {
			t.Fatalf("decoded WireRevision = %d, want %d", m.WireRevision, rev)
		}
	}

	// Pre-revision payload: a stamped meta truncated before the trailing u16
	// (the kit ≤ v2.7.x shape) decodes as revision 0. The chop also takes the
	// controls u16 count that now trails the revision (wire revision 6).
	b := wire.EncodeMeta(wire.Meta{Slug: "x", Name: "X", MinPlayers: 1, MaxPlayers: 1, WireRevision: wire.Revision})
	old, err := decodeMeta(b[:len(b)-4])
	if err != nil {
		t.Fatal(err)
	}
	if old.WireRevision != 0 {
		t.Fatalf("pre-field payload decoded WireRevision = %d, want 0", old.WireRevision)
	}
}

func FuzzDecodeFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, wire.FrameBytes))
	f.Add(make([]byte, wire.FrameBytes-1))
	f.Add(make([]byte, wire.FrameBytes+1))
	f.Fuzz(func(t *testing.T, b []byte) {
		g, err := decodeFrame(b)
		if err == nil && len(b) != wire.FrameBytes {
			t.Fatalf("accepted %d-byte frame, want exactly %d", len(b), wire.FrameBytes)
		}
		_ = g
	})
}

func FuzzDecodeResult(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Add(wire.EncodeResult(wire.Result{Rankings: []wire.Ranking{{PlayerIdx: 0, Metric: 42, Rank: 1}}}))
	f.Add(wire.EncodeResult(wire.Result{Rankings: []wire.Ranking{{PlayerIdx: 200, Metric: 1, Rank: 1}}}))
	roster := fuzzRoster()
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := decodeResult(b, roster, sdk.ModeQuick)
		if err != nil {
			return
		}
		for _, pr := range res.Rankings { // an accepted result only names roster players
			ok := false
			for _, p := range roster {
				if pr.Player == p {
					ok = true
				}
			}
			if !ok {
				t.Fatalf("decoded ranking for non-roster player %+v", pr.Player)
			}
		}
	})
}

// TestDecodeResultRejectsOutOfRoster pins the trust-boundary check: a guest
// naming an index past the callback roster is an error, not a panic or a
// stray account write.
func TestDecodeResultRejectsOutOfRoster(t *testing.T) {
	b := wire.EncodeResult(wire.Result{Rankings: []wire.Ranking{{PlayerIdx: 2, Metric: 1, Rank: 1}}})
	if _, err := decodeResult(b, fuzzRoster(), sdk.ModeQuick); err == nil {
		t.Fatal("out-of-roster player index accepted")
	}
}
