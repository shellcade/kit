package gameabi

import (
	"context"
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/host/sdk"
	"github.com/shellcade/kit/v2/wire"
)

// The game-kind trailer lands on sdk.GameMeta, defaulting to game for older
// payloads and refusing malformed declarations at load (the kit SDKs cannot
// produce them — they fail at encode time).
func TestDecodeMetaGameKind(t *testing.T) {
	b := wire.EncodeMeta(wire.Meta{Slug: "casino", Name: "C", MinPlayers: 1, MaxPlayers: 4,
		GameKind: wire.GameKindCasino, MaxPayoutMultiplier: 10000})
	m, err := decodeMeta(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != sdk.GameKindCasino || m.MaxPayoutMultiplier != 10000 {
		t.Fatalf("decoded kind/mult = %v/%d, want casino/10000", m.Kind, m.MaxPayoutMultiplier)
	}

	// A pre-revision-7 payload reads as game-kind.
	old, err := decodeMeta(b[:len(b)-5])
	if err != nil {
		t.Fatal(err)
	}
	if old.Kind != sdk.GameKindGame || old.MaxPayoutMultiplier != 0 {
		t.Fatalf("pre-kind payload = %v/%d, want game/0", old.Kind, old.MaxPayoutMultiplier)
	}

	// A casino declaration without a payout ceiling is a malformed artifact.
	bad := wire.EncodeMeta(wire.Meta{Slug: "bad", Name: "B", MinPlayers: 1, MaxPlayers: 4,
		GameKind: wire.GameKindCasino})
	if _, err := decodeMeta(bad); err == nil || !strings.Contains(err.Error(), "game kind") {
		t.Fatalf("casino-without-multiplier decode err = %v, want game-kind refusal", err)
	}
}

// fakeCredits is a canned sdk.CreditsService for the gating test.
type fakeCredits struct {
	balance int64
	err     error
	calls   int
}

func (f *fakeCredits) Balance(context.Context, sdk.Player) (int64, error) {
	f.calls++
	return f.balance, f.err
}
func (f *fakeCredits) Wager(context.Context, sdk.Player, int64) error { f.calls++; return f.err }
func (f *fakeCredits) Settle(context.Context, sdk.Player, int64) error {
	f.calls++
	return f.err
}

// creditsCall's gating and error mapping: game-kind guests, out-of-roster
// indices, and a host without an economy are refused BEFORE any service
// call; service errors map onto the ABI status codes.
func TestCreditsCallGating(t *testing.T) {
	player := sdk.Player{AccountID: "acct", Handle: "p"}
	room := sdk.NewTestRoomFor(struct{ sdk.Base }{}, sdk.RoomConfig{Capacity: 1}, sdk.Services{})
	balance := func(ctx context.Context, svc sdk.CreditsService, p sdk.Player) (int64, error) {
		return svc.Balance(ctx, p)
	}
	newHandler := func(kind sdk.GameKind, svc sdk.CreditsService) *wasmHandler {
		return &wasmHandler{
			game:   &wasmGame{meta: sdk.GameMeta{Slug: "g", Kind: kind}},
			roster: []sdk.Player{player},
			cur:    room,
			svc:    sdk.Services{Credits: svc},
		}
	}
	ctx := context.Background()

	// Game-kind guests are rejected before the service is consulted.
	fc := &fakeCredits{balance: 1000}
	h := newHandler(sdk.GameKindGame, fc)
	if got := h.creditsCall(ctx, 0, balance); got != wire.CreditsErrDenied {
		t.Fatalf("game-kind call = %d, want denied", got)
	}
	if fc.calls != 0 {
		t.Fatal("game-kind call reached the credits service")
	}

	// A casino guest on a host without an economy degrades, never traps.
	h = newHandler(sdk.GameKindCasino, nil)
	if got := h.creditsCall(ctx, 0, balance); got != wire.CreditsErrDisabled {
		t.Fatalf("no-economy call = %d, want disabled", got)
	}

	// Out-of-roster indices are refused.
	h = newHandler(sdk.GameKindCasino, fc)
	if got := h.creditsCall(ctx, 5, balance); got != wire.CreditsErrDenied {
		t.Fatalf("out-of-roster call = %d, want denied", got)
	}

	// The happy path returns the service value.
	if got := h.creditsCall(ctx, 0, balance); got != 1000 {
		t.Fatalf("balance call = %d, want 1000", got)
	}

	// Service errors map onto the ABI status codes.
	for _, tc := range []struct {
		err  error
		want int64
	}{
		{sdk.ErrInsufficientCredits, wire.CreditsErrInsufficient},
		{sdk.ErrEconomyDisabled, wire.CreditsErrDisabled},
		{sdk.ErrCreditsDenied, wire.CreditsErrDenied},
		{context.DeadlineExceeded, wire.CreditsErrUnavailable},
	} {
		h = newHandler(sdk.GameKindCasino, &fakeCredits{err: tc.err})
		if got := h.creditsCall(ctx, 0, balance); got != tc.want {
			t.Errorf("err %v maps to %d, want %d", tc.err, got, tc.want)
		}
	}
}
