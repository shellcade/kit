package gameabi

import (
	"fmt"

	"github.com/shellcade/kit/v2/wire"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/sdk"
)

// The host side of the ABI codecs: thin mappings between wire types (the
// canonical encodings owned by the public gamekit module) and engine types.

var modeCode = map[sdk.Mode]uint8{sdk.ModeQuick: wire.ModeQuick, sdk.ModePrivate: wire.ModePrivate, sdk.ModeSolo: wire.ModeSolo}

// wirePlayer maps one roster member onto its wire shape. The character is
// mapped unconditionally — the kit encoder emits the per-member character
// section iff the guest's declared features carry wire.CtxFeatCharacter, so
// non-declaring guests still get byte-identical encodings.
func wirePlayer(p sdk.Player) wire.Player {
	k := wire.KindGuest
	if p.Kind == sdk.KindMember {
		k = wire.KindMember
	}
	return wire.Player{
		Handle: p.Handle, AccountID: p.AccountID, Conn: p.Conn, Kind: k,
		Character: wire.Character{
			Glyph: p.Character.Glyph,
			InkR:  p.Character.InkR, InkG: p.Character.InkG, InkB: p.Character.InkB,
			BgR: p.Character.BgR, BgG: p.Character.BgG, BgB: p.Character.BgB,
			Fallback: p.Character.Fallback,
		},
	}
}

// encodeCtx packs the CallContext for one callback. roster is the roster the
// host will resolve player indices against for the duration of the callback.
// features is the guest's meta-declared Ctx feature bitset (it selects the
// per-member encoding; masked against wire.KnownCtxFeatures defensively,
// matching decodeMeta's posture).
func encodeCtx(nowUnixNanos int64, cfg sdk.RoomConfig, roster []sdk.Player, settled bool, features uint32) *wire.Buf {
	c := wire.Ctx{
		NowUnixNanos: nowUnixNanos,
		Seed:         cfg.Seed,
		SeedSet:      cfg.SeedSet,
		Mode:         modeCode[cfg.Mode],
		Capacity:     uint16(cfg.Capacity),
		MinPlayers:   uint16(cfg.MinPlayers),
		Settled:      settled,
	}
	for _, p := range roster {
		c.Members = append(c.Members, wirePlayer(p))
	}
	var w wire.Buf
	wire.EncodeCtxFeat(&w, c, features&wire.KnownCtxFeatures)
	return &w
}

// decodeMeta maps a packed wire.Meta onto sdk.GameMeta.
func decodeMeta(b []byte) (sdk.GameMeta, error) {
	wm, err := wire.DecodeMeta(b)
	if err != nil {
		return sdk.GameMeta{}, err
	}
	m := sdk.GameMeta{
		Slug:              wm.Slug,
		Name:              wm.Name,
		ShortDescription:  wm.ShortDescription,
		MinPlayers:        int(wm.MinPlayers),
		MaxPlayers:        int(wm.MaxPlayers),
		Tags:              wm.Tags,
		QuickModeLabel:    wm.QuickModeLabel,
		SoloModeLabel:     wm.SoloModeLabel,
		PrivateInviteLine: wm.PrivateInviteLine,
	}
	if wm.HasLeaderboard {
		m.Leaderboard = &sdk.LeaderboardSpec{
			MetricLabel: wm.MetricLabel,
			Direction:   sdk.Direction(wm.Direction),
			Aggregation: sdk.Aggregation(wm.Aggregation),
			Format:      sdk.MetricFormat(wm.Format),
		}
	}
	// Declared config specs must satisfy the ABI authoring rules; a violation
	// is a malformed artifact (kit SDKs can't produce one — they fail at
	// encode time), refused at load like a bad slug.
	if err := wire.ValidateConfigSpecs(wm.ConfigSpecs); err != nil {
		return sdk.GameMeta{}, fmt.Errorf("gameabi: meta config specs: %w", err)
	}
	for _, cs := range wm.ConfigSpecs {
		m.Config = append(m.Config, sdk.ConfigKeySpec{
			Key:         cs.Key,
			Title:       cs.Title,
			Description: cs.Description,
			Type:        sdk.ConfigType(cs.Type),
			Default:     cs.Default,
			Schema:      cs.Schema,
		})
	}
	// Large-room trailer (minor addition): the features bitset is carried
	// verbatim (the host honors bits it implements and ignores the rest —
	// tolerant in both directions); a declared heartbeat outside the
	// envelope is a malformed artifact (kit SDKs fail at encode time).
	if err := wire.ValidateMetaTrailer(wm.CtxFeatures&wire.KnownCtxFeatures, wm.HeartbeatMS); err != nil {
		return sdk.GameMeta{}, fmt.Errorf("gameabi: meta trailer: %w", err)
	}
	m.CtxFeatures = wm.CtxFeatures
	m.HeartbeatMS = int(wm.HeartbeatMS)
	// Lifecycle is tolerant in the host direction (game-abi: values this
	// host does not implement read as resumable) — forward compatibility
	// with future lifecycles; kit SDKs reject undefined values at encode.
	if wm.Lifecycle <= wire.LifecycleResident {
		m.Lifecycle = sdk.Lifecycle(wm.Lifecycle)
	}
	// Wire-revision field (minor addition): carried verbatim — a trailing
	// presence-guarded u16 the SDK encoders stamp with the kit's
	// wire.Revision (absent = 0 = unknown, kit ≤ v2.7.x). The catalog
	// compares it against this host's wire.Revision and warns on artifacts
	// ahead of the host (the deploy-order rule's mechanical anchor).
	m.WireRevision = wm.WireRevision
	// Declared controls (minor addition): validated like config specs — kit
	// SDKs can't produce a violation (they fail at meta() encode time), so
	// one here is a malformed artifact, refused at load like a bad slug.
	if err := wire.ValidateControls(wm.Controls); err != nil {
		return sdk.GameMeta{}, fmt.Errorf("gameabi: meta controls: %w", err)
	}
	for _, cd := range wm.Controls {
		m.Controls = append(m.Controls, sdk.ControlDecl{
			Kind:  sdk.InputKind(cd.Kind),
			Rune:  cd.Rune,
			Key:   sdk.Key(cd.Key),
			Label: cd.Label,
		})
	}
	return m, nil
}

// encodeCtxEpoch packs the CallContext in roster-epoch mode (guests that
// declare wire.CtxFeatRosterEpoch). When full is false the member list is
// not built at all — the member section is 6 bytes regardless of roster
// size, which is the entire point. features is the guest's meta-declared Ctx
// feature bitset (masked like encodeCtx).
func encodeCtxEpoch(nowUnixNanos int64, cfg sdk.RoomConfig, roster []sdk.Player, settled bool, epoch uint32, full bool, features uint32) *wire.Buf {
	c := wire.Ctx{
		NowUnixNanos: nowUnixNanos,
		Seed:         cfg.Seed,
		SeedSet:      cfg.SeedSet,
		Mode:         modeCode[cfg.Mode],
		Capacity:     uint16(cfg.Capacity),
		MinPlayers:   uint16(cfg.MinPlayers),
		Settled:      settled,
	}
	if full {
		for _, p := range roster {
			c.Members = append(c.Members, wirePlayer(p))
		}
	}
	var w wire.Buf
	wire.EncodeCtxEpochFeat(&w, c, epoch, full, features&wire.KnownCtxFeatures)
	return &w
}

// decodeFrame decodes a packed cell array into a canvas.Grid. A wrong-length
// payload is an error (the caller drops the frame with a log, never panics).
func decodeFrame(b []byte) (canvas.Grid, error) {
	g := canvas.New()
	if err := wire.CheckFrame(b); err != nil {
		return g, err
	}
	i := 0
	for row := 0; row < wire.Rows; row++ {
		for col := 0; col < wire.Cols; col++ {
			wc := wire.GetCell(b, i)
			var c canvas.Cell
			c.Rune = wc.Rune
			c.Cp2 = wc.Cp2
			c.Cp3 = wc.Cp3
			if wc.FGSet {
				c.FG = canvas.RGB(wc.FGR, wc.FGG, wc.FGB)
			}
			if wc.BGSet {
				c.BG = canvas.RGB(wc.BGR, wc.BGG, wc.BGB)
			}
			c.Attr = canvas.Attr(wc.Attr)
			c.Cont = wc.Cont
			g.Set(row, col, c)
			i++
		}
	}
	return g, nil
}

var statusByCode = map[uint8]sdk.Status{
	wire.StatusFinished: sdk.StatusFinished,
	wire.StatusDNF:      sdk.StatusDNF,
	wire.StatusFlagged:  sdk.StatusFlagged,
}

// decodeResult decodes a guest result payload against the callback roster.
func decodeResult(b []byte, roster []sdk.Player, mode sdk.Mode) (sdk.Result, error) {
	wr, err := wire.DecodeResult(b)
	if err != nil {
		return sdk.Result{}, err
	}
	res := sdk.Result{Mode: mode}
	for _, rk := range wr.Rankings {
		if int(rk.PlayerIdx) >= len(roster) {
			return sdk.Result{}, fmt.Errorf("gameabi: result player index %d out of roster %d", rk.PlayerIdx, len(roster))
		}
		res.Rankings = append(res.Rankings, sdk.PlayerResult{
			Player: roster[rk.PlayerIdx],
			Metric: int(rk.Metric),
			Rank:   int(rk.Rank),
			Status: statusByCode[rk.Status],
		})
	}
	return res, nil
}
