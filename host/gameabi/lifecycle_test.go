package gameabi

// Lifecycle conformance at the wasm seam: a resident room's guest keeps
// receiving wakes with ZERO members; a non-resident room's empty wakes are
// suppressed (the historical behavior). Observed via CallbackSplit — the
// guest-call accounting advances only when wakes actually reach the guest.

import (
	"testing"
	"time"

	"github.com/shellcade/kit/v2/host/sdk"
)

func tickEmptyRoom(t *testing.T, lifecycle sdk.Lifecycle) (delta time.Duration) {
	t.Helper()
	g := loadFixture(t, Options{})
	cfg := sdk.RoomConfig{Mode: sdk.ModeQuick, Capacity: 2, MinPlayers: 1, Seed: 7, SeedSet: true, Lifecycle: lifecycle}
	h := g.NewRoom(cfg, sdk.Services{Log: quietLog()})
	tr := sdk.NewTestRoomFor(h, cfg, sdk.Services{Log: quietLog()})
	tr.Start() // one callback: start

	before, _ := CallbackSplit(h)
	for i := 0; i < 10; i++ {
		tr.Advance(50 * time.Millisecond)
		tr.Tick()
	}
	after, _ := CallbackSplit(h)
	return after - before
}

func TestResidentWakesWhileEmpty(t *testing.T) {
	if d := tickEmptyRoom(t, sdk.LifecycleResident); d == 0 {
		t.Fatal("resident room's empty wakes never reached the guest")
	}
}

func TestNonResidentEmptyWakesSuppressed(t *testing.T) {
	if d := tickEmptyRoom(t, sdk.LifecycleResumable); d != 0 {
		t.Fatalf("empty non-resident room's wakes reached the guest (%v of guest time)", d)
	}
}
