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
				Rune: c.Rune, Cp2: c.Cp2, Cp3: c.Cp3,
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

// ---- frame diffing state (ABI v2) -------------------------------------------

// rosterCap is the fixed compile-time roster ceiling for per-index baselines.
// At 24-byte cells the baseline table is (rosterCap+broadcast)*FrameBytes ≈
// 0.78 MB of guest linear memory, lazily grown, far under the 32 MiB cap.
const rosterCap = 16

// Per-consumer SDK baseline state, allocated once and reused forever (leaking-GC
// safe; one room per instance + serial callbacks ⇒ no locking). Each slot holds
// the last full frame the SDK sent that consumer (24-byte canonical cells), the
// host-returned epoch it was stamped with, and a present flag. Index rosterCap
// is the broadcast (Identical) slot. deltaScratch is the pre-sized worst-case
// (keyframe-sized) delta buffer written by index — never wire.Buf, which grows.
var (
	baselines       [rosterCap + 1][wire.FrameBytes]byte
	baselineEpoch   [rosterCap + 1]uint32
	baselinePresent [rosterCap + 1]bool
	deltaScratch    [wire.MaxDeltaBytes]byte
)

// broadcastSlot is the index of the Identical baseline within the arrays above.
const broadcastSlot = rosterCap

// invalidateBaselines clears every per-index and broadcast present flag, forcing
// the next send to each slot to a keyframe. Called on any roster change (D7):
// indices renumber, so the host clears all and the guest mirrors.
func invalidateBaselines() {
	for i := range baselinePresent {
		baselinePresent[i] = false
	}
}

// buildSendPayload diffs the freshly packed frame `packed` against slot `slot`'s
// baseline into deltaScratch and returns the payload bytes to send. It applies
// the v2 rules: a keyframe when the slot is not present (first send / rejection
// / roster change) OR when the encoded delta would meet or exceed KeyframeBytes
// (the inclusive budget rule); otherwise a run-coalesced delta. The epoch is the
// slot's currently stamped epoch (0 on a fresh instance). It allocates nothing.
func buildSendPayload(slot int, packed []byte) []byte {
	epoch := baselineEpoch[slot]
	if !baselinePresent[slot] {
		n := wire.BuildKeyframe(packed, deltaScratch[:], epoch)
		return deltaScratch[:n]
	}
	n := wire.BuildFrameDelta(baselines[slot][:], packed, deltaScratch[:], epoch)
	if n >= wire.KeyframeBytes {
		n = wire.BuildKeyframe(packed, deltaScratch[:], epoch)
	}
	return deltaScratch[:n]
}

// commitBaseline records `packed` as slot `slot`'s baseline and stamps it with
// the host-returned epoch, marking it present. Called on a successful send.
func commitBaseline(slot int, packed []byte, returnedEpoch uint32) {
	copy(baselines[slot][:], packed)
	baselineEpoch[slot] = returnedEpoch
	baselinePresent[slot] = true
}

// rosterFingerprint is a cheap fixed-scratch identity of the current roster used
// to detect a roster change between callbacks (join/leave/index-shift): the
// member count plus a rolling hash of each member's account id + kind. It needs
// no allocation and survives across callbacks as a package global.
var (
	lastRosterPrint uint64
	lastRosterSet   bool
)

func rosterFingerprint(members []Player) uint64 {
	h := uint64(1469598103934665603) // FNV-1a offset
	mix := func(b byte) { h ^= uint64(b); h *= 1099511628211 }
	mix(byte(len(members)))
	mix(byte(len(members) >> 8))
	for _, p := range members {
		for i := 0; i < len(p.AccountID); i++ {
			mix(p.AccountID[i])
		}
		mix(0)
		mix(byte(p.Kind))
		mix('|')
	}
	return h
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
