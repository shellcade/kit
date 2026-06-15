package memsvc_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/shellcade/kit/v2/host/memsvc"
	"github.com/shellcade/kit/v2/host/sdk"
)

func quietFactory(t *testing.T) *memsvc.Factory {
	t.Helper()
	return memsvc.NewFactory(slog.New(slog.NewTextHandler(io.Discard, nil)), sdk.NewRegistry())
}

func member(id, handle string) sdk.Player {
	return sdk.Player{AccountID: id, Handle: handle, Kind: sdk.KindMember}
}

// New returns a usable factory backed by the default registry: For yields a
// fully-populated Services bundle (no nil concern) tagged to the slug.
func TestNewBuildsServices(t *testing.T) {
	f := memsvc.New()
	svc := f.For("room-1", "demo")
	if svc.Leaderboard == nil || svc.Accounts == nil || svc.Config == nil ||
		svc.Chat == nil || svc.Spectate == nil || svc.Log == nil {
		t.Fatalf("For returned a Services with a nil concern: %+v", svc)
	}
}

// A per-user KVStore round-trips, is namespaced per (slug, account, key), and a
// Get returns an independent copy (mutating it must not corrupt the store).
func TestKVNamespacingAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := quietFactory(t)

	storeFor := func(slug string, p sdk.Player) sdk.KVStore {
		return f.For("r", slug).Accounts.For(p).Store()
	}
	ada := member("acct-ada", "ada")
	bob := member("acct-bob", "bob")

	if err := storeFor("g1", ada).Set(ctx, "k", []byte("ada-g1"), sdk.MergeKeepWinner); err != nil {
		t.Fatal(err)
	}
	if err := storeFor("g2", ada).Set(ctx, "k", []byte("ada-g2"), sdk.MergeKeepWinner); err != nil {
		t.Fatal(err)
	}
	if err := storeFor("g1", bob).Set(ctx, "k", []byte("bob-g1"), sdk.MergeKeepWinner); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		slug string
		p    sdk.Player
		want string
	}{
		{"g1", ada, "ada-g1"},
		{"g2", ada, "ada-g2"}, // different slug, same account+key: isolated
		{"g1", bob, "bob-g1"}, // different account, same slug+key: isolated
	}
	for _, tc := range cases {
		got, ok, err := storeFor(tc.slug, tc.p).Get(ctx, "k")
		if err != nil || !ok {
			t.Fatalf("Get(%s/%s) ok=%v err=%v", tc.slug, tc.p.AccountID, ok, err)
		}
		if string(got) != tc.want {
			t.Fatalf("Get(%s/%s)=%q want %q", tc.slug, tc.p.AccountID, got, tc.want)
		}
		got[0] = 'X' // mutate the returned copy; the store must not see it
	}
	got, _, _ := storeFor("g1", ada).Get(ctx, "k")
	if string(got) != "ada-g1" {
		t.Fatalf("Get returned an aliased slice: store corrupted to %q", got)
	}

	// A missing key reads not-found (so the game falls back to its default).
	if _, ok, err := storeFor("g1", ada).Get(ctx, "absent"); ok || err != nil {
		t.Fatalf("missing key ok=%v err=%v, want false,nil", ok, err)
	}
	// Delete removes the key; a second Delete is a no-op.
	if err := storeFor("g1", ada).Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := storeFor("g1", ada).Get(ctx, "k"); ok {
		t.Fatal("key present after Delete")
	}
	if err := storeFor("g1", ada).Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete of absent key = %v, want nil (no-op)", err)
	}
}

// A `max` key is kept monotonic on WRITE: a lower (or equal) write never
// regresses the stored value, but a higher one wins. Non-integer values fall
// through to last-writer-wins (no monotonic guard to apply).
func TestKVMaxMonotonicOnWrite(t *testing.T) {
	ctx := context.Background()
	f := quietFactory(t)
	kv := f.For("r", "g").Accounts.For(member("a", "a")).Store()

	steps := []struct {
		write string
		want  string
	}{
		{"5", "5"},
		{"3", "5"},   // lower: ignored
		{"5", "5"},   // equal: ignored
		{"9", "9"},   // higher: wins
		{"7", "9"},   // lower again: ignored
		{"12", "12"}, // higher: wins
	}
	for i, s := range steps {
		if err := kv.Set(ctx, "peak", []byte(s.write), sdk.MergeMax); err != nil {
			t.Fatal(err)
		}
		got, _, _ := kv.Get(ctx, "peak")
		if string(got) != s.want {
			t.Fatalf("step %d write %s: got %q want %q", i, s.write, got, s.want)
		}
	}

	// Non-integer under max: no monotonic guard, last write wins.
	if err := kv.Set(ctx, "name", []byte("zed"), sdk.MergeMax); err != nil {
		t.Fatal(err)
	}
	if err := kv.Set(ctx, "name", []byte("abe"), sdk.MergeMax); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := kv.Get(ctx, "name"); string(got) != "abe" {
		t.Fatalf("non-integer max: got %q want last-writer-wins abe", got)
	}
}

// Merge folds the loser's KV into the winner. On a key collision the winner's
// recorded rule governs; a loser-only key moves across unchanged. This is the
// table proving the merge-rule semantics (MergeMax keeps the larger int,
// MergeSum adds, keep-winner/keep-loser pick a side, non-integer sum/max
// degrades to keep-winner).
func TestMergeRuleSemantics(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		winnerVal string // "" => winner has no such key (loser-only)
		rule      sdk.MergeRule
		loserVal  string
		want      string
	}{
		{"keep-winner default", "win", sdk.MergeKeepWinner, "lose", "win"},
		{"empty rule is keep-winner", "win", sdk.MergeRule(""), "lose", "win"},
		{"unknown rule is keep-winner", "win", sdk.MergeRule("bogus"), "lose", "win"},
		{"keep-loser", "win", sdk.MergeKeepLoser, "lose", "lose"},
		{"sum adds ints", "10", sdk.MergeSum, "7", "17"},
		{"sum negative", "10", sdk.MergeSum, "-3", "7"},
		{"max keeps larger (loser bigger)", "5", sdk.MergeMax, "9", "9"},
		{"max keeps larger (winner bigger)", "9", sdk.MergeMax, "5", "9"},
		{"max equal", "4", sdk.MergeMax, "4", "4"},
		{"sum non-integer degrades to keep-winner", "win", sdk.MergeSum, "3", "win"},
		{"max non-integer degrades to keep-winner", "5", sdk.MergeMax, "lose", "5"},
		{"loser-only key moves across", "", sdk.MergeSum, "42", "42"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := quietFactory(t)
			winner := member("winner", "W")
			loser := member("loser", "L")
			winStore := f.For("r", "g").Accounts.For(winner).Store()
			loseStore := f.For("r", "g").Accounts.For(loser).Store()

			if tc.winnerVal != "" {
				// Seed the winner's key with the rule under test. Use keep-winner to
				// seed so the `max` write-time monotonic guard never rewrites the seed.
				if err := winStore.Set(ctx, "k", []byte(tc.winnerVal), tc.rule); err != nil {
					t.Fatal(err)
				}
			}
			if err := loseStore.Set(ctx, "k", []byte(tc.loserVal), tc.rule); err != nil {
				t.Fatal(err)
			}

			f.Merge(winner.AccountID, loser.AccountID)

			got, ok, err := winStore.Get(ctx, "k")
			if err != nil || !ok {
				t.Fatalf("winner key after merge ok=%v err=%v", ok, err)
			}
			if string(got) != tc.want {
				t.Fatalf("merged value = %q, want %q", got, tc.want)
			}
			// The loser's row is always consumed by the merge.
			if _, ok, _ := loseStore.Get(ctx, "k"); ok {
				t.Fatal("loser key still present after merge")
			}
		})
	}
}

// Merge is namespaced per slug and a no-op for degenerate account ids.
func TestMergeIsolationAndGuards(t *testing.T) {
	ctx := context.Background()
	f := quietFactory(t)
	winner := member("w", "w")
	loser := member("l", "l")

	// A loser key under slug g2 must not bleed into the winner under g1.
	if err := f.For("r", "g1").Accounts.For(winner).Store().Set(ctx, "k", []byte("1"), sdk.MergeSum); err != nil {
		t.Fatal(err)
	}
	if err := f.For("r", "g2").Accounts.For(loser).Store().Set(ctx, "k", []byte("99"), sdk.MergeSum); err != nil {
		t.Fatal(err)
	}
	f.Merge(winner.AccountID, loser.AccountID)
	if got, _, _ := f.For("r", "g1").Accounts.For(winner).Store().Get(ctx, "k"); string(got) != "1" {
		t.Fatalf("cross-slug bleed: g1 winner = %q want untouched 1", got)
	}
	// The g2 loser key moved to the g2 winner unchanged (loser-only there).
	if got, ok, _ := f.For("r", "g2").Accounts.For(winner).Store().Get(ctx, "k"); !ok || string(got) != "99" {
		t.Fatalf("g2 loser-only key = %q ok=%v, want 99", got, ok)
	}

	// Guards: self-merge and empty ids do nothing (and don't panic).
	f.Merge("w", "w")
	f.Merge("", "l")
	f.Merge("w", "")
}

// Account identity passes through Player; Kind distinguishes guest from member.
func TestAccountIdentity(t *testing.T) {
	f := quietFactory(t)
	acc := f.For("r", "g").Accounts.For(member("acct-1", "ada"))
	if acc.ID() != "acct-1" || acc.Handle() != "ada" || acc.Kind() != sdk.KindMember {
		t.Fatalf("account identity = (%q,%q,%q)", acc.ID(), acc.Handle(), acc.Kind())
	}
	guest := f.For("r", "g").Accounts.For(sdk.Player{AccountID: "", Handle: "anon", Kind: sdk.KindGuest})
	if guest.Kind() != sdk.KindGuest {
		t.Fatalf("guest kind = %q want %q", guest.Kind(), sdk.KindGuest)
	}
}

// Per-game config is slug-bound and read-only: SetConfig seeds it, a game reads
// only its own slug's keys, and a missing key reads not-found.
func TestConfigSlugBound(t *testing.T) {
	ctx := context.Background()
	f := quietFactory(t)
	f.SetConfig("pokies", "odds", []byte(`{"rtp":0.95}`))
	f.SetConfig("other", "odds", []byte(`{"rtp":0.10}`))

	got, ok, err := f.For("r", "pokies").Config.Get(ctx, "odds")
	if err != nil || !ok {
		t.Fatalf("config Get ok=%v err=%v", ok, err)
	}
	if string(got) != `{"rtp":0.95}` {
		t.Fatalf("config = %q want pokies value", got)
	}
	got[0] = 'X' // returned copy must be independent
	if again, _, _ := f.For("r", "pokies").Config.Get(ctx, "odds"); string(again) != `{"rtp":0.95}` {
		t.Fatalf("config Get returned an aliased slice: store corrupted to %q", again)
	}
	// A different game's config store cannot read pokies' key.
	if _, ok, _ := f.For("r", "thirdgame").Config.Get(ctx, "odds"); ok {
		t.Fatal("config leaked across slug boundary")
	}
	// A missing key reads not-found.
	if _, ok, _ := f.For("r", "pokies").Config.Get(ctx, "absent"); ok {
		t.Fatal("absent config key read as present")
	}
}

// Leaderboard recording: every account-bound result is recorded tagged by mode +
// status and surfaces through the Reader; guests (empty AccountID) are dropped.
func TestLeaderboardRecordingAndReader(t *testing.T) {
	ctx := context.Background()
	reg := sdk.NewRegistry()
	if err := reg.Add(testGame{slug: "race"}); err != nil {
		t.Fatal(err)
	}
	f := memsvc.NewFactory(slog.New(slog.NewTextHandler(io.Discard, nil)), reg)
	lb := f.For("room-x", "race").Leaderboard

	lb.Post("race", sdk.Result{
		Mode: sdk.ModeQuick,
		Rankings: []sdk.PlayerResult{
			{Player: member("acct-ada", "ada"), Metric: 60, Status: sdk.StatusFinished},
			{Player: member("acct-bob", "bob"), Metric: 90, Status: sdk.StatusFinished},
			{Player: sdk.Player{Kind: sdk.KindGuest, Handle: "anon"}, Metric: 999, Status: sdk.StatusFinished},
		},
	})

	st, err := f.Reader().Standings(ctx, "race", sdk.AllTime, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 {
		t.Fatalf("standings len=%d want 2 (guest dropped)", len(st))
	}
	// Default spec is HigherBetter: bob (90) ranks above ada (60).
	if st[0].AccountID != "acct-bob" || st[0].Rank != 1 || st[0].Value != 90 {
		t.Fatalf("rank 1 = %+v, want bob/1/90", st[0])
	}
	if st[1].AccountID != "acct-ada" || st[1].Handle != "ada" {
		t.Fatalf("rank 2 = %+v, want ada", st[1])
	}
}

// testGame is a minimal sdk.Game for registry-backed Reader tests. It embeds
// sdk.GameBase (sealed marker) and sdk.Base (Handler marker) so it satisfies
// both interfaces without re-declaring their growable surface.
type testGame struct {
	sdk.GameBase
	slug string
}

func (g testGame) Meta() sdk.GameMeta { return sdk.GameMeta{Slug: g.slug, Name: g.slug} }
func (g testGame) NewRoom(cfg sdk.RoomConfig, svc sdk.Services) sdk.Handler {
	return sdk.Base{} // the Reader path never builds a room; identity only
}
