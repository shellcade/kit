package conformance

import (
	"strings"
	"testing"

	"github.com/shellcade/kit/v2/host/canvas"
	"github.com/shellcade/kit/v2/host/sdk"
)

// TestDiffDetailNamesCell: when post-checkpoint frames diverge, the hibernation
// failure message must name the offending frame and cell so an author can find
// the non-deterministic idiom instead of staring at an opaque FAIL.
func TestDiffDetailNamesCell(t *testing.T) {
	var ctrl, hib sdk.Frame
	ctrl.Cells[3][7] = sdk.Cell{Rune: 'A'}
	hib.Cells[3][7] = sdk.Cell{Rune: 'B'}

	got := diffDetail([]sdk.Frame{ctrl}, []sdk.Frame{hib})
	for _, want := range []string{"frame 0", "row 3", "col 7", `"A"`, `"B"`} {
		if !strings.Contains(got, want) {
			t.Errorf("diff detail missing %q\n got: %s", want, got)
		}
	}
}

// TestDiffDetailFrameCount: a frame-count mismatch is reported as a path
// divergence (a callback took a different branch after restore).
func TestDiffDetailFrameCount(t *testing.T) {
	got := diffDetail([]sdk.Frame{{}, {}}, []sdk.Frame{{}})
	if !strings.Contains(got, "pushed 1") || !strings.Contains(got, "pushed 2") {
		t.Errorf("frame-count detail did not name both counts: %s", got)
	}
}

// TestDiffDetailStyleOnly: identical runes but a differing style is named as a
// styling divergence (color/attr) rather than a rune mismatch.
func TestDiffDetailStyleOnly(t *testing.T) {
	var ctrl, hib sdk.Frame
	ctrl.Cells[0][0] = sdk.Cell{Rune: 'X', Attr: canvas.AttrBold}
	hib.Cells[0][0] = sdk.Cell{Rune: 'X'}
	got := diffDetail([]sdk.Frame{ctrl}, []sdk.Frame{hib})
	if !strings.Contains(got, "styling") {
		t.Errorf("style-only divergence not named: %s", got)
	}
}
