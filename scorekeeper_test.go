package kit_test

import (
	"testing"

	kit "github.com/shellcade/kit/v2"
	"github.com/shellcade/kit/v2/kittest"
)

func TestScoreKeeperRecordOnImprovePostsOnlyOnNewHigh(t *testing.T) {
	p := kittest.Player("acct-1")
	r := kittest.NewRoom(p)
	sk := kit.NewScoreKeeper(kit.OnImprove)

	sk.Record(r, p, 10) // first ever -> posts 10
	sk.Record(r, p, 5)  // lower -> no post
	sk.Record(r, p, 12) // new high -> posts 12

	if len(r.Posted) != 2 {
		t.Fatalf("want 2 posts, got %d: %+v", len(r.Posted), r.Posted)
	}
	if r.Posted[0].Rankings[0].Metric != 10 || r.Posted[1].Rankings[0].Metric != 12 {
		t.Fatalf("unexpected metrics: %+v", r.Posted)
	}
	if r.Posted[1].Rankings[0].Status != kit.StatusFinished {
		t.Fatalf("live posts should be StatusFinished, got %v", r.Posted[1].Rankings[0].Status)
	}
}

func TestScoreKeeperRecordOnChangePostsEveryChange(t *testing.T) {
	p := kittest.Player("acct-1")
	r := kittest.NewRoom(p)
	sk := kit.NewScoreKeeper(kit.OnChange)

	sk.Record(r, p, 3)
	sk.Record(r, p, 3) // unchanged -> no post
	sk.Record(r, p, 1) // changed (even though lower) -> posts

	if len(r.Posted) != 2 {
		t.Fatalf("want 2 posts, got %d: %+v", len(r.Posted), r.Posted)
	}
	if r.Posted[1].Rankings[0].Metric != 1 {
		t.Fatalf("want last metric 1, got %+v", r.Posted[1].Rankings[0])
	}
}

func TestScoreKeeperFlushLeavePostsDNFThenNoOp(t *testing.T) {
	p := kittest.Player("acct-1")
	r := kittest.NewRoom(p)
	sk := kit.NewScoreKeeper(kit.OnImprove)

	sk.Record(r, p, 7) // posts 7 Finished
	sk.FlushLeave(r, p, kit.StatusDNF)

	last := r.Posted[len(r.Posted)-1].Rankings[0]
	if last.Metric != 7 || last.Status != kit.StatusDNF {
		t.Fatalf("want metric=7 DNF, got %+v", last)
	}

	before := len(r.Posted)
	sk.FlushLeave(r, p, kit.StatusDNF) // untracked now -> no-op
	if len(r.Posted) != before {
		t.Fatalf("flush after leave should be a no-op, got %d posts", len(r.Posted))
	}
}

func TestScoreKeeperFlushAllPostsDeterministicOrder(t *testing.T) {
	a := kittest.Player("acct-b")
	b := kittest.Player("acct-a")
	r := kittest.NewRoom(a, b)
	sk := kit.NewScoreKeeper(kit.OnChange)

	sk.Record(r, a, 1)
	sk.Record(r, b, 2)
	r.Posted = nil // ignore live posts; assert FlushAll alone
	sk.FlushAll(r, kit.StatusDNF)

	if len(r.Posted) != 2 {
		t.Fatalf("want 2 posts, got %d: %+v", len(r.Posted), r.Posted)
	}
	// Deterministic: sorted by AccountID -> acct-a then acct-b.
	if r.Posted[0].Rankings[0].Player.AccountID != "acct-a" ||
		r.Posted[1].Rankings[0].Player.AccountID != "acct-b" {
		t.Fatalf("FlushAll must post in AccountID order, got %q then %q",
			r.Posted[0].Rankings[0].Player.AccountID, r.Posted[1].Rankings[0].Player.AccountID)
	}
	if r.Posted[0].Rankings[0].Status != kit.StatusDNF {
		t.Fatalf("FlushAll status not propagated: %+v", r.Posted[0].Rankings[0])
	}
}

func TestScoreKeeperPersistBestWritesMergeMax(t *testing.T) {
	p := kittest.Player("acct-1")
	r := kittest.NewRoom(p)
	sk := kit.NewScoreKeeper(kit.OnImprove)

	sk.PersistBest(r, p, "best", 42)

	if got := string(r.KV["acct-1"]["best"]); got != "42" {
		t.Fatalf(`want KV best="42", got %q`, got)
	}
	if rule := r.KVRules["acct-1"]["best"]; rule != kit.MergeMax {
		t.Fatalf("want MergeMax, got %v", rule)
	}
}

func TestScoreKeeperPersistWalletWritesSumAndMax(t *testing.T) {
	p := kittest.Player("acct-1")
	r := kittest.NewRoom(p)
	sk := kit.NewScoreKeeper(kit.OnImprove)

	sk.PersistWallet(r, p, "balance", 150, "peak", 900)

	if got := string(r.KV["acct-1"]["balance"]); got != "150" {
		t.Fatalf(`want balance="150", got %q`, got)
	}
	if rule := r.KVRules["acct-1"]["balance"]; rule != kit.MergeSum {
		t.Fatalf("balance want MergeSum, got %v", rule)
	}
	if got := string(r.KV["acct-1"]["peak"]); got != "900" {
		t.Fatalf(`want peak="900", got %q`, got)
	}
	if rule := r.KVRules["acct-1"]["peak"]; rule != kit.MergeMax {
		t.Fatalf("peak want MergeMax, got %v", rule)
	}
}
