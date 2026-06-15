package blobstore

import (
	"context"
	"errors"
	"testing"
)

// failingStore errors every operation — the outcome=fail half of the seam.
type failingStore struct{}

var errInjected = errors.New("injected store failure")

func (failingStore) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errInjected
}
func (failingStore) Put(context.Context, string, []byte) error { return errInjected }
func (failingStore) Delete(context.Context, string) error      { return errInjected }
func (failingStore) List(context.Context, string) ([]string, error) {
	return nil, errInjected
}

// opRecorder counts rec calls by op/outcome, standing in for
// metrics.Metrics.BlobstoreOp.
type opRecorder map[string]int

func (r opRecorder) rec(op string, ok bool) {
	outcome := "fail"
	if ok {
		outcome = "ok"
	}
	r[op+"/"+outcome]++
}

// Instrument records every op with its outcome and passes results through
// untouched; a Get of a missing key is ok (absence is an answer, not a store
// failure).
func TestInstrumentRecordsOps(t *testing.T) {
	ctx := context.Background()
	rec := opRecorder{}
	s := Instrument(NewMemory(), rec.rec)

	if err := s.Put(ctx, "snapshots/r1", []byte("blob")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if data, ok, err := s.Get(ctx, "snapshots/r1"); err != nil || !ok || string(data) != "blob" {
		t.Fatalf("Get = %q ok=%v err=%v", data, ok, err)
	}
	if _, ok, err := s.Get(ctx, "snapshots/missing"); err != nil || ok {
		t.Fatalf("Get(missing) = ok=%v err=%v, want false nil", ok, err)
	}
	if keys, err := s.List(ctx, "snapshots/"); err != nil || len(keys) != 1 {
		t.Fatalf("List = %v err=%v", keys, err)
	}
	if err := s.Delete(ctx, "snapshots/r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := opRecorder{"put/ok": 1, "get/ok": 2, "list/ok": 1, "delete/ok": 1}
	for k, n := range want {
		if rec[k] != n {
			t.Errorf("rec[%s] = %d, want %d (all: %v)", k, rec[k], n, rec)
		}
	}
	for k := range rec {
		if want[k] == 0 {
			t.Errorf("unexpected recording %s=%d", k, rec[k])
		}
	}
}

func TestInstrumentRecordsFailures(t *testing.T) {
	ctx := context.Background()
	rec := opRecorder{}
	s := Instrument(failingStore{}, rec.rec)

	if _, _, err := s.Get(ctx, "k"); !errors.Is(err, errInjected) {
		t.Fatalf("Get err = %v, want injected", err)
	}
	if err := s.Put(ctx, "k", nil); !errors.Is(err, errInjected) {
		t.Fatalf("Put err = %v, want injected", err)
	}
	if err := s.Delete(ctx, "k"); !errors.Is(err, errInjected) {
		t.Fatalf("Delete err = %v, want injected", err)
	}
	if _, err := s.List(ctx, "k"); !errors.Is(err, errInjected) {
		t.Fatalf("List err = %v, want injected", err)
	}
	for _, k := range []string{"get/fail", "put/fail", "delete/fail", "list/fail"} {
		if rec[k] != 1 {
			t.Errorf("rec[%s] = %d, want 1 (all: %v)", k, rec[k], rec)
		}
	}
}

// A nil recorder (or store) wires through unchanged — callers instrument
// unconditionally.
func TestInstrumentNilPassthrough(t *testing.T) {
	mem := NewMemory()
	if got := Instrument(mem, nil); got != Store(mem) {
		t.Fatal("Instrument(s, nil) must return s unchanged")
	}
	if got := Instrument(nil, opRecorder{}.rec); got != nil {
		t.Fatal("Instrument(nil, rec) must return nil")
	}
}
