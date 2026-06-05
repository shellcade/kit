package game

import "github.com/shellcade/kit/v2/wire"

// The guest side of the ABI codecs: thin mappings between wire types (the
// canonical encodings owned by gamekit/wire) and the authoring types.

// callContext is the decoded per-callback room state.
type callContext struct {
	nowUnixNanos int64
	cfg          RoomConfig
	members      []Player
	settled      bool
}

// decodeCtx decodes a CallContext and returns the reader positioned at the
// event-specific extra payload.
func decodeCtx(b []byte) (callContext, *wire.Rd) {
	r := &wire.Rd{B: b}
	wc := wire.DecodeCtx(r)
	c := callContext{
		nowUnixNanos: wc.NowUnixNanos,
		cfg: RoomConfig{
			Mode:       Mode(wc.Mode),
			Capacity:   int(wc.Capacity),
			MinPlayers: int(wc.MinPlayers),
			Seed:       wc.Seed,
			SeedSet:    wc.SeedSet,
		},
		settled: wc.Settled,
	}
	for _, p := range wc.Members {
		c.members = append(c.members, Player{
			AccountID: p.AccountID, Handle: p.Handle, Conn: p.Conn, Kind: Kind(p.Kind),
		})
	}
	return c, r
}

// encodeMeta packs GameMeta for the meta export.
func encodeMeta(m GameMeta) []byte {
	wm := wire.Meta{
		Slug:              m.Slug,
		Name:              m.Name,
		ShortDescription:  m.ShortDescription,
		MinPlayers:        uint16(m.MinPlayers),
		MaxPlayers:        uint16(m.MaxPlayers),
		Tags:              m.Tags,
		QuickModeLabel:    m.QuickModeLabel,
		SoloModeLabel:     m.SoloModeLabel,
		PrivateInviteLine: m.PrivateInviteLine,
	}
	if m.Leaderboard != nil {
		wm.HasLeaderboard = true
		wm.MetricLabel = m.Leaderboard.MetricLabel
		wm.Direction = uint8(m.Leaderboard.Direction)
		wm.Aggregation = uint8(m.Leaderboard.Aggregation)
		wm.Format = uint8(m.Leaderboard.Format)
	}
	return wire.EncodeMeta(wm)
}

// frameScratch is the reused frame encode buffer: one room per instance and
// serial callbacks mean no concurrent encodes, and reuse keeps the steady
// state allocation-free (important under TinyGo's GC).
var frameScratch [wire.FrameBytes]byte

// encodeFrame packs a Frame into the scratch buffer (valid until the next call).
func encodeFrame(f *Frame) []byte {
	i := 0
	for row := 0; row < Rows; row++ {
		for col := 0; col < Cols; col++ {
			c := &f.Cells[row][col]
			wire.PutCell(frameScratch[:], i, wire.Cell{
				Rune:  c.Rune,
				FGSet: c.FG.set, FGR: c.FG.r, FGG: c.FG.g, FGB: c.FG.b,
				BGSet: c.BG.set, BGR: c.BG.r, BGG: c.BG.g, BGB: c.BG.b,
				Attr: uint8(c.Attr),
				Cont: c.Cont,
			})
			i++
		}
	}
	return frameScratch[:]
}

// encodeResult packs a Result against the current roster (player -> index).
func encodeResult(res Result, roster []Player) []byte {
	var wr wire.Result
	for _, pr := range res.Rankings {
		idx := 0
		for i, p := range roster {
			if p == pr.Player {
				idx = i
				break
			}
		}
		wr.Rankings = append(wr.Rankings, wire.Ranking{
			PlayerIdx: uint32(idx),
			Metric:    int64(pr.Metric),
			Rank:      uint16(pr.Rank),
			Status:    uint8(pr.Status),
		})
	}
	return wire.EncodeResult(wr)
}
