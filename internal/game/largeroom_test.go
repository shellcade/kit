package game

import (
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

func resetRosterState() {
	rosterCache = nil
	rosterCacheBytes = nil
	rosterCacheEpoch = 0
	rosterCacheEpochSet = false
	epochMismatch = false
	epochMismatchLogged = false
}

func epochCtxPayload(epoch uint32, full bool, members ...wire.Player) []byte {
	var w wire.Buf
	wire.EncodeCtxEpoch(&w, wire.Ctx{
		NowUnixNanos: 1, Seed: 7, SeedSet: true, Capacity: 1000, MinPlayers: 1,
		Members: members,
	}, epoch, full)
	return w.B
}

// Sentinel-form decode: full form caches at its epoch; unchanged form reuses
// the cache with zero member allocations; a new full form re-decodes.
func TestRosterEpochCache(t *testing.T) {
	resetRosterState()
	ada := wire.Player{Handle: "ada", AccountID: "a", Conn: "c1", Kind: 1}
	bob := wire.Player{Handle: "bob", AccountID: "b", Conn: "c2", Kind: 1}

	c1, _, changed := decodeCtx(epochCtxPayload(1, true, ada))
	if !changed || len(c1.members) != 1 || c1.members[0].AccountID != "a" {
		t.Fatalf("full form: changed=%v members=%+v", changed, c1.members)
	}
	if !rosterCacheEpochSet || rosterCacheEpoch != 1 {
		t.Fatalf("cache epoch = %d set=%v", rosterCacheEpoch, rosterCacheEpochSet)
	}

	allocs := testing.AllocsPerRun(50, func() {
		c, _, ch := decodeCtx(epochCtxPayload(1, false))
		if ch || len(c.members) != 1 {
			t.Fatalf("unchanged form: changed=%v members=%d", ch, len(c.members))
		}
	})
	// The decode path allocates the callContext/reader bookkeeping but must
	// not allocate member strings; allow the small fixed overhead.
	if allocs > 4 {
		t.Fatalf("unchanged-form decode allocates %v/op — member strings are leaking into the hot path", allocs)
	}

	c3, _, changed := decodeCtx(epochCtxPayload(2, true, ada, bob))
	if !changed || len(c3.members) != 2 || c3.members[1].AccountID != "b" {
		t.Fatalf("mutated full form: changed=%v members=%+v", changed, c3.members)
	}
}

// An unchanged form whose epoch doesn't match the cache is a host fault:
// flagged for a one-line log, cache retained, baselines invalidated.
func TestRosterEpochMismatchDegrades(t *testing.T) {
	resetRosterState()
	ada := wire.Player{Handle: "ada", AccountID: "a", Conn: "c1", Kind: 1}

	if _, _, changed := decodeCtx(epochCtxPayload(5, true, ada)); !changed {
		t.Fatal("seed full form not marked changed")
	}
	c, _, changed := decodeCtx(epochCtxPayload(9, false)) // wrong epoch
	if !changed {
		t.Fatal("mismatch must be conservative (changed=true)")
	}
	if !epochMismatch {
		t.Fatal("mismatch not flagged for logging")
	}
	if len(c.members) != 1 || c.members[0].AccountID != "a" {
		t.Fatalf("cache not retained on mismatch: %+v", c.members)
	}
}

// Meta encode: trailer fields ride through; invalid declarations panic.
func TestMetaTrailerEncode(t *testing.T) {
	meta := GameMeta{Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 1000,
		CtxFeatures: CtxFeatRosterEpoch, HeartbeatMS: 100}
	b := encodeMeta(meta)
	wm, err := wire.DecodeMeta(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wm.CtxFeatures != wire.CtxFeatRosterEpoch || wm.HeartbeatMS != 100 {
		t.Fatalf("trailer = %#x %d", wm.CtxFeatures, wm.HeartbeatMS)
	}

	assertPanics := func(name string, m GameMeta, want string) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("%s: no panic", name)
			}
			if msg, _ := r.(string); !strings.Contains(msg, want) {
				t.Fatalf("%s: panic %q, want substring %q", name, r, want)
			}
		}()
		encodeMeta(m)
	}
	assertPanics("bad heartbeat", GameMeta{Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 2, HeartbeatMS: 5}, "HeartbeatMS")
	assertPanics("unknown feature bit", GameMeta{Slug: "g", Name: "G", MinPlayers: 1, MaxPlayers: 2, CtxFeatures: 1 << 9}, "undefined bit")
}
