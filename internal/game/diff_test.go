package game

import (
	"bytes"
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

// These tests exercise the SDK diff helpers (codec.go) directly — the pieces
// Room.Send/Identical (wasm-gated) wire together — against a mock host that
// holds a per-slot baseline + epoch and applies the container exactly as the
// real host does. This proves the per-consumer baseline reconciliation logic
// without a wasm runtime.

type mockHost struct {
	prev    [rosterCap + 1][]byte
	epoch   [rosterCap + 1]uint32
	present [rosterCap + 1]bool
	seq     uint32
}

func newMockHost() *mockHost {
	h := &mockHost{}
	for i := range h.prev {
		h.prev[i] = make([]byte, wire.FrameBytes)
	}
	return h
}

// recv mirrors the host send/identical fn: validate, apply iff keyframe or
// (present && epoch matches), else drop+bump. Returns the slot's epoch.
func (h *mockHost) recv(slot int, payload []byte) uint32 {
	if err := wire.CheckFrameDelta(payload); err != nil {
		h.seq++
		h.epoch[slot] = h.seq
		return h.epoch[slot]
	}
	if wire.IsKeyframe(payload) {
		_ = wire.ApplyFrameDelta(h.prev[slot], payload)
		h.present[slot] = true
		h.epoch[slot] = wire.DeltaEpoch(payload)
		return h.epoch[slot]
	}
	if !h.present[slot] || wire.DeltaEpoch(payload) != h.epoch[slot] {
		h.seq++
		h.epoch[slot] = h.seq
		return h.epoch[slot]
	}
	_ = wire.ApplyFrameDelta(h.prev[slot], payload)
	return h.epoch[slot]
}

// sdkSend replicates room.Send's body against the mock host for slot idx.
func sdkSend(h *mockHost, idx int, f *Frame) {
	packed := encodeFrame(f)
	sentEpoch := baselineEpoch[idx]
	payload := buildSendPayload(idx, packed)
	cp := append([]byte(nil), payload...) // copy: deltaScratch is reused
	returned := h.recv(idx, cp)
	if returned != sentEpoch && baselinePresent[idx] {
		baselinePresent[idx] = false
		baselineEpoch[idx] = returned
		return
	}
	commitBaseline(idx, packed, returned)
}

// sdkIdentical replicates room.Identical's body against the mock host.
func sdkIdentical(h *mockHost, f *Frame) {
	packed := encodeFrame(f)
	sentEpoch := baselineEpoch[broadcastSlot]
	payload := buildSendPayload(broadcastSlot, packed)
	cp := append([]byte(nil), payload...)
	returned := h.recv(broadcastSlot, cp)
	if returned != sentEpoch && baselinePresent[broadcastSlot] {
		baselinePresent[broadcastSlot] = false
		baselineEpoch[broadcastSlot] = returned
		return
	}
	commitBaseline(broadcastSlot, packed, returned)
	for i := 0; i < rosterCap; i++ {
		commitBaseline(i, packed, returned)
	}
}

func resetDiffState() {
	for i := range baselinePresent {
		baselinePresent[i] = false
		baselineEpoch[i] = 0
		for j := range baselines[i] {
			baselines[i][j] = 0
		}
	}
	lastRosterSet = false
	lastRosterPrint = 0
}

func frameWith(text string) *Frame {
	f := NewFrame()
	f.Text(0, 0, text, Style{})
	return f
}

func TestFirstSendIsKeyframeThenDelta(t *testing.T) {
	resetDiffState()
	h := newMockHost()
	f1 := frameWith("hello")

	// First send: slot not present -> keyframe. Host reconstructs == packed.
	packed1 := append([]byte(nil), encodeFrame(f1)...)
	sdkSend(h, 0, f1)
	if !baselinePresent[0] {
		t.Fatal("baseline not present after first send")
	}
	if !bytes.Equal(h.prev[0], packed1) {
		t.Fatal("host frame != packed after keyframe")
	}

	// Second send, one cell changed: a delta, and the host reconstruction equals
	// the new packed frame.
	f2 := frameWith("hellp")
	packed2 := append([]byte(nil), encodeFrame(f2)...)
	sdkSend(h, 0, f2)
	if !bytes.Equal(h.prev[0], packed2) {
		t.Fatal("host frame != packed after delta")
	}
}

func TestRejectionSelfHeals(t *testing.T) {
	resetDiffState()
	h := newMockHost()
	f := frameWith("abc")
	sdkSend(h, 0, f) // keyframe, present, epoch 0

	// Simulate a host epoch bump out-of-band (e.g. hibernation re-seed): the
	// next delta will mismatch and be rejected, forcing the guest to keyframe.
	h.present[0] = false
	h.seq = 5
	h.epoch[0] = 6

	f2 := frameWith("abd")
	packed2 := append([]byte(nil), encodeFrame(f2)...)
	sdkSend(h, 0, f2) // guest still thinks epoch 0 -> delta rejected
	if baselinePresent[0] {
		t.Fatal("guest should drop present after rejection")
	}
	// Next send heals: keyframe stamped with the returned epoch.
	f3 := frameWith("abe")
	packed3 := append([]byte(nil), encodeFrame(f3)...)
	sdkSend(h, 0, f3)
	if !bytes.Equal(h.prev[0], packed3) {
		t.Fatal("self-heal keyframe did not reconstruct")
	}
	_ = packed2
}

func TestIdenticalReconcilesAllSlotsThenSend(t *testing.T) {
	resetDiffState()
	h := newMockHost()
	fb := frameWith("board")
	packedB := append([]byte(nil), encodeFrame(fb)...)

	sdkIdentical(h, fb) // keyframe on broadcast; reconcile every per-index slot
	for i := 0; i < rosterCap; i++ {
		if !baselinePresent[i] {
			t.Fatalf("slot %d not present after Identical", i)
		}
	}
	if !bytes.Equal(h.prev[broadcastSlot], packedB) {
		t.Fatal("broadcast host frame mismatch")
	}

	// A per-player Send now must diff against the broadcast baseline and apply
	// cleanly on the per-index host slot — but the host slot was reconciled by
	// our SDK only; the host's per-index prev still differs. Seed the host's
	// per-index slot to match the broadcast (as the real host does on Identical).
	for i := 0; i < rosterCap; i++ {
		copy(h.prev[i], h.prev[broadcastSlot])
		h.present[i] = true
		h.epoch[i] = h.epoch[broadcastSlot]
	}

	f2 := frameWith("boaXd")
	packed2 := append([]byte(nil), encodeFrame(f2)...)
	sdkSend(h, 2, f2)
	if !bytes.Equal(h.prev[2], packed2) {
		t.Fatal("per-player send after Identical did not reconstruct")
	}
}

func TestRosterChangeInvalidates(t *testing.T) {
	resetDiffState()
	p1 := rosterFingerprint([]Player{{AccountID: "a", Kind: KindMember}})
	p2 := rosterFingerprint([]Player{{AccountID: "a", Kind: KindMember}, {AccountID: "b", Kind: KindMember}})
	if p1 == p2 {
		t.Fatal("roster fingerprint collision on join")
	}
	// Same roster -> same print.
	if p1 != rosterFingerprint([]Player{{AccountID: "a", Kind: KindMember}}) {
		t.Fatal("fingerprint not stable for identical roster")
	}
	// invalidateBaselines clears present.
	baselinePresent[0] = true
	baselinePresent[broadcastSlot] = true
	invalidateBaselines()
	if baselinePresent[0] || baselinePresent[broadcastSlot] {
		t.Fatal("invalidateBaselines did not clear present")
	}
}
