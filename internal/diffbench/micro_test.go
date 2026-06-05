package diffbench

import "testing"

// micro benchmarks mirror the Rust diff-rs examples/bench.rs cases EXACTLY (same
// four change densities, single prev->next pair, RUN-LIST) so the Go ns/frame
// and Rust ns/frame are directly comparable (the matrix BenchmarkEncode reports
// per-SEQUENCE ns/op, which mixes densities). Run:
//
//	go test -run '^$' -bench BenchmarkMicro -benchmem ./internal/diffbench/
func benchPair(b *testing.B, prev, next []byte) {
	dst := make([]byte, MaxEncoded)
	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for i := 0; i < b.N; i++ {
		sink += encodeRunList(prev, next, dst)
	}
	_ = sink
}

func BenchmarkMicroNoChange(b *testing.B) {
	prev := blankFrame()
	next := dup(prev)
	benchPair(b, prev, next)
}

func BenchmarkMicroSingleCell(b *testing.B) {
	prev := blankFrame()
	next := dup(prev)
	putRune(next, 12*Cols+23, '_')
	benchPair(b, prev, next)
}

func BenchmarkMicroOneRow(b *testing.B) {
	prev := blankFrame()
	next := dup(prev)
	for c := 0; c < Cols; c++ {
		putRune(next, 23*Cols+c, '#')
	}
	benchPair(b, prev, next)
}

func BenchmarkMicroFullChange(b *testing.B) {
	prev := blankFrame()
	next := blankFrame()
	for i := 0; i < FrameCells; i++ {
		putRune(prev, i, 'A')
		putRune(next, i, 'B')
	}
	benchPair(b, prev, next)
}
