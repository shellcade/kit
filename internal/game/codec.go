package game

import (
	"bytes"

	"github.com/shellcade/kit/v2/wire"
)

// The guest side of the ABI codecs: thin mappings between wire types (the
// canonical encodings owned by gamekit/wire) and the authoring types.

// callContext is the decoded per-callback room state.
type callContext struct {
	nowUnixNanos int64
	cfg          RoomConfig
	members      []Player
	settled      bool
}

// Roster cache: the host re-sends the full member list in EVERY callback
// payload, but rosters change only on join/leave/index-shift. Decoding N
// members afresh per callback is O(N) string allocations per input/wake —
// GC pressure under -gc=conservative (the recommended build profile), and
// under -gc=leaking builds those allocations are PERMANENT, so a long-lived
// large room leaks its roster at callback rate (load testing measured ~100KB
// leaked per callback at 1000 players, OOMing the guest within seconds of
// play).
//
// Instead, the raw wire bytes of the member section are compared against the
// previous callback's (a ~100B/member memcmp); on a match the previously
// decoded []Player is reused with ZERO allocation, and only a real roster
// change re-decodes. The byte compare is strictly stronger than the old
// rosterFingerprint hash (which this replaces): any join/leave/index-shift/
// kind-change alters the bytes.
//
// Lifetime contract: the members slice handed to a callback (and returned by
// Room.Members()) is valid for the DURATION OF THAT CALLBACK; a later roster
// change re-decodes into the same backing array. Games that retain players
// across callbacks must copy the Player values they keep (kit games key
// long-lived state by AccountID already).
var (
	rosterCache      []Player
	rosterCacheBytes []byte

	// Roster-epoch mode state (games declaring CtxFeatRosterEpoch): the epoch
	// the cached roster was decoded at. epochMismatch flags a host-fault
	// unchanged-form whose epoch didn't match the cache (decodeCall logs it);
	// epochMismatchLogged keeps that warning to one line per instance.
	rosterCacheEpoch    uint32
	rosterCacheEpochSet bool
	epochMismatch       bool
	epochMismatchLogged bool
)

// declaredCtxFeatures is the registered game's GameMeta.CtxFeatures, set by
// Run. The host encodes per-member character sections iff the guest's meta
// declares CtxFeatCharacter — there is no in-band discriminator — so the
// decoder must know the declaration to read (and skim) the member section
// with the right shape.
var declaredCtxFeatures uint32

// decodeCtx decodes a CallContext and returns the reader positioned at the
// event-specific extra payload, plus whether the roster changed since the
// previous callback (true on the first callback).
func decodeCtx(b []byte) (callContext, *wire.Rd, bool) {
	r := &wire.Rd{B: b}
	c := callContext{nowUnixNanos: r.I64()}
	c.cfg.Seed = r.I64()
	c.cfg.SeedSet = r.Bool()
	c.cfg.Mode = Mode(r.U8())
	c.cfg.Capacity = int(r.U16())
	c.cfg.MinPlayers = int(r.U16())

	count := r.U16()
	var changed bool
	switch count {
	case wire.CtxRosterUnchanged:
		// Roster-epoch sentinel: epoch only, no member data. Reuse the cache;
		// an epoch mismatch is a host fault — degrade (keep the cache), the
		// single warning is emitted by decodeCall, which has a Room to log on.
		epoch := r.U32()
		if !rosterCacheEpochSet || epoch != rosterCacheEpoch {
			epochMismatch = true
			changed = true // be conservative: invalidate baselines
		}
	case wire.CtxRosterFull:
		// Roster-epoch sentinel: full roster at an epoch. Authoritative.
		epoch := r.U32()
		decodeMembersInto(r, int(r.U16()))
		rosterCacheEpoch = epoch
		rosterCacheEpochSet = true
		rosterCacheBytes = nil // bytes cache is legacy-mode state
		changed = true
	default:
		// Legacy full roster (pre-feature hosts): skim the member section's
		// extent without decoding, memcmp against the previous callback's
		// bytes, and re-decode only on a real change.
		start := r.Off - 2 // include the count in the compared region
		for i := 0; i < int(count) && !r.Bad; i++ {
			r.SkipStr() // handle
			r.SkipStr() // account id
			r.SkipStr() // conn
			r.U8()      // kind
			if declaredCtxFeatures&wire.CtxFeatCharacter != 0 {
				// Character section (host sends it iff our meta declares the
				// feature): glyph str + 7 fixed bytes (ink RGB, bg RGB,
				// fallback). The skim must skip EXACTLY what
				// decodeMembersInto reads or the memcmp region misaligns.
				r.SkipStr() // glyph
				for j := 0; j < 7; j++ {
					r.U8()
				}
			}
		}
		region := r.B[start:r.Off]
		changed = rosterCacheBytes == nil || !bytes.Equal(region, rosterCacheBytes)
		if changed {
			rr := &wire.Rd{B: region}
			decodeMembersInto(rr, int(rr.U16()))
			if r.Bad {
				// Malformed member section: don't prime the cache — every
				// malformed callback stays "changed" and decodes what it can
				// (the pre-cache behavior). A well-formed region is ≥2 bytes
				// (the count), so a primed cache is never nil.
				rosterCacheBytes = nil
			} else {
				rosterCacheBytes = append(rosterCacheBytes[:0], region...)
			}
			rosterCacheEpochSet = false // epoch state is sentinel-mode only
		}
	}
	c.members = rosterCache

	c.settled = r.Bool()
	return c, r, changed
}

// decodeMembersInto re-decodes the member list into the shared rosterCache
// backing array (the only place member strings are allocated).
func decodeMembersInto(r *wire.Rd, n int) {
	rosterCache = rosterCache[:0]
	for i := 0; i < n && !r.Bad; i++ {
		var p Player
		p.Handle = r.Str()
		p.AccountID = r.Str()
		p.Conn = r.Str()
		p.Kind = Kind(r.U8())
		// keep the legacy skim in decodeCtx in lockstep with this section
		if declaredCtxFeatures&wire.CtxFeatCharacter != 0 {
			p.Character.Glyph = r.Str()
			p.Character.InkR = r.U8()
			p.Character.InkG = r.U8()
			p.Character.InkB = r.U8()
			p.Character.BgR = r.U8()
			p.Character.BgG = r.U8()
			p.Character.BgB = r.U8()
			p.Character.Fallback = r.U8()
		}
		rosterCache = append(rosterCache, p)
	}
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
	for _, cs := range m.Config {
		wm.ConfigSpecs = append(wm.ConfigSpecs, wire.ConfigSpec{
			Key:         cs.Key,
			Title:       cs.Title,
			Description: cs.Description,
			Type:        uint8(cs.Type),
			Default:     cs.Default,
			Schema:      cs.Schema,
		})
	}
	// Declared specs are validated here so an authoring mistake fails loudly
	// at meta() time — surfaced by `shellcade-kit check`, the dev runner, and
	// the host's load-time throwaway instance — same fail-fast posture as a
	// compiled-in default that doesn't compile.
	if err := wire.ValidateConfigSpecs(wm.ConfigSpecs); err != nil {
		panic("kit: invalid GameMeta.Config: " + err.Error())
	}
	// Large-room trailer: ctx-feature bits + declared heartbeat, validated
	// under the same fail-fast posture.
	if m.HeartbeatMS < 0 || m.HeartbeatMS > 0xFFFF {
		panic("kit: invalid GameMeta.HeartbeatMS: out of range")
	}
	wm.CtxFeatures = m.CtxFeatures
	wm.HeartbeatMS = uint16(m.HeartbeatMS)
	if err := wire.ValidateMetaTrailer(wm.CtxFeatures, wm.HeartbeatMS); err != nil {
		panic("kit: invalid GameMeta: " + err.Error())
	}
	wm.Lifecycle = uint8(m.Lifecycle)
	if err := wire.ValidateLifecycle(wm.Lifecycle, wm.MinPlayers); err != nil {
		panic("kit: invalid GameMeta: " + err.Error())
	}
	// Stamp the wire revision this kit was built against — not
	// author-settable; the host uses it to warn on or refuse artifacts
	// declaring a revision above its own (deploy-order enforcement and
	// per-artifact provenance, ABI.md §4.2 / §5).
	wm.WireRevision = wire.Revision
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

// rosterCap is the fixed compile-time roster ceiling for per-index baselines,
// adopted from the contract constant wire.RosterCap (1024 supports large-room
// games; the SDK SILENTLY DROPS Send for an index >= rosterCap, so the cap
// must comfortably exceed any real roster).
// Guest linear memory stays proportional to the ACTIVE roster, not the cap:
// per-slot baselines are lazily allocated on first commit — ~45 KiB per
// actively-sent-to consumer instead of a ~47 MiB static table.
const rosterCap = wire.RosterCap

// Per-consumer SDK baseline state, allocated once and reused forever (leaking-GC
// safe; one room per instance + serial callbacks ⇒ no locking). Each slot holds
// the last full frame the SDK sent that consumer (24-byte canonical cells), the
// host-returned epoch it was stamped with, and a present flag. Index rosterCap
// is the broadcast (Identical) slot. deltaScratch is the pre-sized worst-case
// (keyframe-sized) delta buffer written by index — never wire.Buf, which grows.
// Baseline slots are nil until first committed (lazy; see rosterCap note).
var (
	baselines       [rosterCap + 1][]byte // wire.FrameBytes each once allocated
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
	n := wire.BuildFrameDelta(baselines[slot], packed, deltaScratch[:], epoch)
	if n >= wire.KeyframeBytes {
		n = wire.BuildKeyframe(packed, deltaScratch[:], epoch)
	}
	return deltaScratch[:n]
}

// commitBaseline records `packed` as slot `slot`'s baseline and stamps it with
// the host-returned epoch, marking it present. Called on a successful send.
// The slot buffer is allocated on first commit (lazy; leaking-GC safe because
// a slot is allocated at most once and reused forever).
func commitBaseline(slot int, packed []byte, returnedEpoch uint32) {
	if baselines[slot] == nil {
		baselines[slot] = make([]byte, wire.FrameBytes)
	}
	copy(baselines[slot], packed)
	baselineEpoch[slot] = returnedEpoch
	baselinePresent[slot] = true
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
