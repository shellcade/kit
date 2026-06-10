package diffbench

import (
	"bytes"
	"testing"

	"github.com/shellcade/kit/v2/wire"
)

// TestReferenceEncoderMatchesProductionWire pins the middle link of the
// crossverify reference chain. The committed golden vectors
// (crossverify/tests/golden/*.dgld) are emitted from THIS package's
// encodeRunList / encodeKeyframe / encodeRunListOrKeyframe, but what a Go
// guest actually ships is wire.BuildFrameDelta / wire.BuildKeyframe (via
// internal/game's codec, with the host-issued epoch). CI's golden-freshness
// gate re-emits the vectors from this package and diffs them against the
// committed set, so it can only police drift in the PRODUCTION encoder if the
// two encoders are byte-identical — which this test asserts, frame by frame,
// across every committed real capture and every synthetic scenario (epoch 0,
// the value the emitter models; the field is fixed-width, so identity at
// epoch 0 plus the header layout covers the wire form).
func TestReferenceEncoderMatchesProductionWire(t *testing.T) {
	real, err := realScenarios()
	if err != nil {
		t.Fatalf("loading real scenarios: %v", err)
	}
	scenarios := append(real, synthScenarios()...)

	benchDst := make([]byte, MaxEncoded)
	wireDst := make([]byte, wire.MaxDeltaBytes)
	for _, s := range scenarios {
		prev := blankFrame()
		for fi, next := range s.Frames {
			n := encodeRunList(prev, next, benchDst)
			wn := wire.BuildFrameDelta(prev, next, wireDst, 0)
			if !bytes.Equal(benchDst[:n], wireDst[:wn]) {
				t.Fatalf("%s frame %d: encodeRunList (%d B) != wire.BuildFrameDelta (%d B) — "+
					"the golden emitter has drifted from the production encoder; reconcile "+
					"them (and regenerate the .dgld vectors if the production bytes are the "+
					"intended ones)", s.Name, fi, n, wn)
			}

			n = encodeKeyframe(prev, next, benchDst)
			wn = wire.BuildKeyframe(next, wireDst, 0)
			if !bytes.Equal(benchDst[:n], wireDst[:wn]) {
				t.Fatalf("%s frame %d: encodeKeyframe (%d B) != wire.BuildKeyframe (%d B)",
					s.Name, fi, n, wn)
			}

			// The fallback golden must equal the production budget rule:
			// run-list, degrading to the keyframe form at >= KeyframeBytes.
			n = encodeRunListOrKeyframe(prev, next, benchDst)
			wn = wire.BuildFrameDelta(prev, next, wireDst, 0)
			if wn >= wire.KeyframeBytes {
				wn = wire.BuildKeyframe(next, wireDst, 0)
			}
			if !bytes.Equal(benchDst[:n], wireDst[:wn]) {
				t.Fatalf("%s frame %d: encodeRunListOrKeyframe (%d B) != production budget rule (%d B)",
					s.Name, fi, n, wn)
			}

			prev = next
		}
	}
}
