package wire

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Cross-language scalar-encoding golden vectors: the generated-vector
// discipline the crossverify .dgld harness applies to the delta encoder,
// extended to the rest of the wire surface (meta with its presence-guarded
// trailing sections, the three ctx member-section forms, results). This
// package is the EMITTER and the freshness gate; the Rust SDK replays the
// committed vectors (rust/src/wire.rs, mod scalar_golden) so a wire-visible
// change on either side fails CI instead of relying on hand-pasted hex being
// regenerated in two places.
//
// Direction-aware coverage:
//   - guest-encoded payloads (meta, result): Rust asserts its encoders are
//     byte-identical to these Go-emitted bytes;
//   - host-encoded payloads (ctx): Rust runs decode_ctx over them, asserting
//     every field AND the reader position at the trailing event-extra;
//   - decoder presence-guards: the meta_trunc_* vectors are truncated
//     OLDER-FORM meta payloads (each ends exactly at a section boundary)
//     pinning DecodeMeta's absent-trailer tolerance (the Rust SDK has no meta
//     decoder — meta decode is host-side — so only Go consumes these).
//
// scalarGoldenPath lives under the Rust crate so its replay test is
// self-contained (include_str!); TestScalarGoldenFresh regenerates the
// content on every plain `go test` run and fails if the committed file has
// gone stale against the current encoders.
const scalarGoldenPath = "../rust/tests/golden/scalars.txt"

// ctxEventExtra is the u32 appended after every ctx vector, standing in for
// the per-export trailing args (e.g. playerIdx): decoding must leave the
// reader exactly there.
const ctxEventExtra uint32 = 7

// ---- fixtures (mirrored verbatim in rust/src/wire.rs mod scalar_golden) ----

// scalarMetaDefault is the default-valued fixture: Rust's Meta::DEFAULT with
// only a slug (MinPlayers/MaxPlayers default to 1 there). WireRevision is the
// SDK-stamped constant, so this vector also pins Go/Rust revision lockstep at
// the byte level.
func scalarMetaDefault() Meta {
	return Meta{Slug: "default", MinPlayers: 1, MaxPlayers: 1, WireRevision: Revision}
}

// scalarMetaFull populates every section: tags, mode labels, leaderboard,
// config specs (incl. a JSON-typed key with a schema), ctx features,
// heartbeat, a non-default lifecycle, and the stamped revision.
func scalarMetaFull() Meta {
	return Meta{
		Slug:              "golden-full",
		Name:              "Golden Full",
		ShortDescription:  "every section populated",
		MinPlayers:        2,
		MaxPlayers:        8,
		Tags:              []string{"multi", "card"},
		QuickModeLabel:    "Deal me in",
		SoloModeLabel:     "Practice",
		PrivateInviteLine: "Join my table",
		HasLeaderboard:    true,
		MetricLabel:       "chips",
		Direction:         1, // lower-better
		Aggregation:       1, // sum-results
		Format:            2, // duration
		ConfigSpecs: []ConfigSpec{
			{
				Key:         "odds-variant",
				Title:       "Odds variant",
				Description: "PAR sheet.",
				Type:        ConfigJSON,
				Default:     `{"name":"Default"}`,
				Schema:      `{"type":"object"}`,
			},
			{Key: "motd", Title: "Banner", Description: "Floor banner.", Type: ConfigText},
		},
		CtxFeatures:  CtxFeatRosterEpoch,
		HeartbeatMS:  250,
		Lifecycle:    LifecycleEphemeral,
		WireRevision: Revision,
	}
}

// scalarMetaTrunc is the basis for the truncated older-form vectors: a
// populated leaderboard block but NO config specs, so the trailing-section
// widths measured from the end are fixed (config 2 | large-room 6 |
// lifecycle 1 | wireRevision 2) and each truncation point is exact. The
// trailer values are non-zero so each successive form demonstrably decodes
// one more section.
func scalarMetaTrunc() Meta {
	return Meta{
		Slug:           "trunc",
		Name:           "Trunc",
		MinPlayers:     1,
		MaxPlayers:     4,
		HasLeaderboard: true,
		MetricLabel:    "score",
		Direction:      1,
		Aggregation:    0,
		Format:         0,
		CtxFeatures:    CtxFeatRosterEpoch,
		HeartbeatMS:    100,
		Lifecycle:      LifecycleEphemeral,
		WireRevision:   Revision,
	}
}

func scalarCtx() Ctx {
	return Ctx{
		NowUnixNanos: 1718000000123456789,
		Seed:         -42,
		SeedSet:      true,
		Mode:         ModePrivate,
		Capacity:     8,
		MinPlayers:   2,
		Members: []Player{
			{Handle: "ada", AccountID: "acct-ada", Conn: "c1", Kind: KindMember},
			{Handle: "guest-7", AccountID: "", Conn: "c2", Kind: KindGuest},
		},
		Settled: true,
	}
}

// scalarResult is a three-player roster with out-of-roster-order rankings and
// mixed statuses; the Rust side reproduces it through encode_outcome's
// player→index mapping, so the indices below are what that mapping must
// yield. (StatusFlagged is host-assigned, never guest-encoded, so the guest
// statuses are Finished/DNF.)
func scalarResult() Result {
	return Result{Rankings: []Ranking{
		{PlayerIdx: 2, Metric: 9000, Rank: 1, Status: StatusFinished},
		{PlayerIdx: 0, Metric: -1, Rank: 2, Status: StatusDNF},
		{PlayerIdx: 1, Metric: 512, Rank: 2, Status: StatusFinished},
	}}
}

// ---- vector generation -------------------------------------------------------

type scalarVector struct {
	name    string
	payload []byte
}

// Trailing meta section widths measured from the END of an encoding with zero
// config specs: the u16 spec count (2) + large-room u32+u16 (6) + lifecycle
// u8 (1) + wireRevision u16 (2).
const (
	truncPreRevision  = 2
	truncPreLifecycle = 2 + 1
	truncPreLargeRoom = 2 + 1 + 6
	truncPreConfig    = 2 + 1 + 6 + 2
)

func scalarVectors() []scalarVector {
	trunc := EncodeMeta(scalarMetaTrunc())

	ctx := func(encode func(w *Buf)) []byte {
		var w Buf
		encode(&w)
		w.U32(ctxEventExtra)
		return w.B
	}

	return []scalarVector{
		{"meta_default", EncodeMeta(scalarMetaDefault())},
		{"meta_full", EncodeMeta(scalarMetaFull())},
		{"meta_trunc_pre_config", trunc[:len(trunc)-truncPreConfig]},
		{"meta_trunc_pre_largeroom", trunc[:len(trunc)-truncPreLargeRoom]},
		{"meta_trunc_pre_lifecycle", trunc[:len(trunc)-truncPreLifecycle]},
		{"meta_trunc_pre_revision", trunc[:len(trunc)-truncPreRevision]},
		{"ctx_legacy", ctx(func(w *Buf) { EncodeCtx(w, scalarCtx()) })},
		{"ctx_epoch_full", ctx(func(w *Buf) { EncodeCtxEpoch(w, scalarCtx(), 42, true) })},
		{"ctx_epoch_unchanged", ctx(func(w *Buf) { EncodeCtxEpoch(w, scalarCtx(), 43, false) })},
		{"result_mixed", EncodeResult(scalarResult())},
	}
}

const scalarGoldenHeader = `# Cross-language scalar-encoding golden vectors (meta / ctx / result), emitted
# by the Go reference encoders in kit/wire (scalar_golden_test.go) and replayed
# by the Rust SDK (rust/src/wire.rs, mod scalar_golden).
#
# DO NOT EDIT BY HAND. When an encoding legitimately changes (a new trailing
# meta section, a wire.Revision bump, ...), review the wire-visible change,
# then regenerate and commit:
#
#   WIRE_SCALAR_GOLDEN_WRITE=1 go test -run TestScalarGoldenFresh ./wire/
#
# Format: one "name = hex" line per vector. meta_* are guest-encoded payloads
# (Rust asserts byte-identity from its own encoders); ctx_* are host-encoded
# payloads with a trailing u32 event-extra (7) (Rust decodes them, asserting
# fields and reader position); meta_trunc_* are truncated older-form metas
# pinning the host-side decoder's presence guards (Go-only — the guest SDKs
# carry no meta decoder).
`

func renderScalarGolden() string {
	var b strings.Builder
	b.WriteString(scalarGoldenHeader)
	for _, v := range scalarVectors() {
		fmt.Fprintf(&b, "%s = %x\n", v.name, v.payload)
	}
	return b.String()
}

// TestScalarGoldenFresh is the freshness gate: the committed vector file must
// equal what the CURRENT encoders emit, so an encoder byte-output change
// cannot silently strand the Rust replay tests on historical bytes. Runs on
// plain `go test` (CI's test job); set WIRE_SCALAR_GOLDEN_WRITE=1 to
// regenerate the file after a reviewed encoding change.
func TestScalarGoldenFresh(t *testing.T) {
	want := renderScalarGolden()
	if os.Getenv("WIRE_SCALAR_GOLDEN_WRITE") != "" {
		if err := os.WriteFile(scalarGoldenPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", scalarGoldenPath, len(want))
		return
	}
	got, err := os.ReadFile(scalarGoldenPath)
	if err != nil {
		t.Fatalf("reading committed scalar golden vectors: %v\nregenerate with:\n  WIRE_SCALAR_GOLDEN_WRITE=1 go test -run TestScalarGoldenFresh ./wire/", err)
	}
	if string(got) != want {
		t.Fatalf("%s is STALE against the current Go encoders.\n"+
			"An encoding's byte output changed — review the change (it is wire-visible\n"+
			"and may need a wire.Revision bump and an ABI.md entry), then regenerate,\n"+
			"commit, and make sure the Rust replay fixtures still describe the same\n"+
			"logical payloads:\n  WIRE_SCALAR_GOLDEN_WRITE=1 go test -run TestScalarGoldenFresh ./wire/",
			scalarGoldenPath)
	}
}

// TestScalarGoldenMetaDecode pins DecodeMeta over the emitted vectors: the
// populated fixtures round-trip field-exact, and each truncated older form
// decodes with exactly the absent trailers zero-valued — the presence-guard
// behavior every future trailing section must preserve.
func TestScalarGoldenMetaDecode(t *testing.T) {
	for _, fix := range []struct {
		name string
		m    Meta
	}{
		{"meta_default", scalarMetaDefault()},
		{"meta_full", scalarMetaFull()},
	} {
		got, err := DecodeMeta(EncodeMeta(fix.m))
		if err != nil {
			t.Fatalf("%s: decode: %v", fix.name, err)
		}
		if !reflect.DeepEqual(got, fix.m) {
			t.Errorf("%s does not round-trip:\n got  %+v\n want %+v", fix.name, got, fix.m)
		}
	}

	full := scalarMetaTrunc()
	enc := EncodeMeta(full)
	cases := []struct {
		name string
		cut  int
		want func(m *Meta) // zero the fields absent from this older form
	}{
		{"meta_trunc_pre_config", truncPreConfig, func(m *Meta) {
			m.ConfigSpecs = nil
			m.CtxFeatures, m.HeartbeatMS, m.Lifecycle, m.WireRevision = 0, 0, 0, 0
		}},
		{"meta_trunc_pre_largeroom", truncPreLargeRoom, func(m *Meta) {
			m.CtxFeatures, m.HeartbeatMS, m.Lifecycle, m.WireRevision = 0, 0, 0, 0
		}},
		{"meta_trunc_pre_lifecycle", truncPreLifecycle, func(m *Meta) {
			m.Lifecycle, m.WireRevision = 0, 0
		}},
		{"meta_trunc_pre_revision", truncPreRevision, func(m *Meta) {
			m.WireRevision = 0
		}},
	}
	for _, tc := range cases {
		got, err := DecodeMeta(enc[:len(enc)-tc.cut])
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		want := full
		tc.want(&want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s presence guards broken:\n got  %+v\n want %+v", tc.name, got, want)
		}
	}
}

// TestScalarGoldenCtxDecode pins DecodeCtx over the three emitted ctx forms,
// including the reader position at the trailing event-extra — the Go twin of
// the Rust replay assertions.
func TestScalarGoldenCtxDecode(t *testing.T) {
	vectors := make(map[string][]byte, len(scalarVectors()))
	for _, v := range scalarVectors() {
		vectors[v.name] = v.payload
	}
	base := scalarCtx()
	cases := []struct {
		name string
		want Ctx
	}{
		{"ctx_legacy", base},
		{"ctx_epoch_full", func() Ctx {
			c := base
			c.RosterEpoch, c.RosterEpochSet = 42, true
			return c
		}()},
		{"ctx_epoch_unchanged", func() Ctx {
			c := base
			c.RosterEpoch, c.RosterEpochSet, c.RosterUnchanged = 43, true, true
			c.Members = nil
			return c
		}()},
	}
	for _, tc := range cases {
		r := &Rd{B: vectors[tc.name]}
		got := DecodeCtx(r)
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

// TestScalarGoldenResultDecode pins DecodeResult over the result vector.
func TestScalarGoldenResultDecode(t *testing.T) {
	got, err := DecodeResult(EncodeResult(scalarResult()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, scalarResult()) {
		t.Errorf("result_mixed does not round-trip:\n got  %+v\n want %+v", got, scalarResult())
	}
}
