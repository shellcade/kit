//go:build !wasip1 && !tinygo.wasm

package game

import (
	"reflect"
	"testing"
)

// feed runs a sequence of byte chunks through one parser, then a final Timeout,
// and returns every event produced (in order).
func feed(chunks ...[]byte) []parsedEvent {
	var p keyParser
	var got []parsedEvent
	for _, c := range chunks {
		got = p.Feed(c, got)
	}
	got = p.Timeout(got)
	return got
}

func TestParsePlainRunes(t *testing.T) {
	got := feed([]byte("abc"))
	want := []parsedEvent{
		{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'a'}},
		{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'b'}},
		{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'c'}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseArrowInOneRead(t *testing.T) {
	got := feed([]byte{0x1b, '[', 'A'})
	want := []parsedEvent{keyEvent(KeyUp)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseArrowSplitAcrossReads(t *testing.T) {
	// The hard case: ESC in one read, "[B" in the next. A correct parser holds
	// state across Feeds and emits exactly one Down.
	got := feed([]byte{0x1b}, []byte{'[', 'B'})
	want := []parsedEvent{keyEvent(KeyDown)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseEscSplitThreeWays(t *testing.T) {
	got := feed([]byte{0x1b}, []byte{'['}, []byte{'C'})
	want := []parsedEvent{keyEvent(KeyRight)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestBareEscResolvesToLeaveOnTimeout(t *testing.T) {
	// ESC with nothing after it: Pending() is true until Timeout flushes a leave.
	var p keyParser
	got := p.Feed([]byte{0x1b}, nil)
	if len(got) != 0 {
		t.Fatalf("bare ESC should emit nothing before timeout, got %+v", got)
	}
	if !p.Pending() {
		t.Fatal("parser should be pending after a lone ESC")
	}
	got = p.Timeout(got)
	if len(got) != 1 || got[0].Kind != evLeave {
		t.Fatalf("timeout should yield a single leave, got %+v", got)
	}
	if p.Pending() {
		t.Fatal("parser should be reset after timeout")
	}
}

func TestEscFollowedByNonBracketIsLeave(t *testing.T) {
	// ESC then a non-'[' byte (e.g. Alt-x) is treated as a bare Escape (leave),
	// matching the runner's "Esc leaves" reservation. Bytes after the leave are
	// not processed (the session is ending).
	got := feed([]byte{0x1b, 'x'})
	if len(got) != 1 || got[0].Kind != evLeave {
		t.Fatalf("ESC+x should be a single leave, got %+v", got)
	}
}

func TestCtrlCLeaves(t *testing.T) {
	got := feed([]byte{0x03})
	if len(got) != 1 || got[0].Kind != evLeave {
		t.Fatalf("Ctrl-C should leave, got %+v", got)
	}
}

func TestCtrlTSwitchesSeat(t *testing.T) {
	got := feed([]byte{0x14})
	if len(got) != 1 || got[0].Kind != evSeatSwitch {
		t.Fatalf("Ctrl-T should switch seat, got %+v", got)
	}
}

func TestEnterTabBackspace(t *testing.T) {
	got := feed([]byte{'\r', '\t', 0x7f, 0x08})
	want := []parsedEvent{
		keyEvent(KeyEnter),
		keyEvent(KeyTab),
		keyEvent(KeyBackspace),
		keyEvent(KeyBackspace),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestPasteBurstDrainsWithoutLoss(t *testing.T) {
	// A paste arrives as one big read; every printable rune must come through,
	// and an embedded arrow sequence must still parse.
	burst := []byte("hi\x1b[Ax")
	got := feed(burst)
	want := []parsedEvent{
		{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'h'}},
		{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'i'}},
		keyEvent(KeyUp),
		{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'x'}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestUnknownCSIDropped(t *testing.T) {
	// A CSI we don't map (e.g. Home = ESC [ H) is dropped, not errored, and the
	// parser returns to ground so following input is fine.
	got := feed([]byte{0x1b, '[', 'H'}, []byte("z"))
	want := []parsedEvent{{Kind: evInput, Input: Input{Kind: InputRune, Rune: 'z'}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestTruncatedCSIDiscardedOnTimeout(t *testing.T) {
	// ESC '[' with no final byte: a genuinely truncated sequence. Timeout drops
	// it (no leave, no stray input) and resets the parser.
	var p keyParser
	out := p.Feed([]byte{0x1b, '['}, nil)
	if !p.Pending() {
		t.Fatal("parser should be pending mid-CSI")
	}
	out = p.Timeout(out)
	if len(out) != 0 {
		t.Fatalf("truncated CSI should be silently dropped, got %+v", out)
	}
	if p.Pending() {
		t.Fatal("parser should reset after timeout")
	}
}
