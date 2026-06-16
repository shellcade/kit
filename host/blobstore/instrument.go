package blobstore

import "context"

// Instrument wraps s so every operation reports its outcome through rec — the
// recording seam for shellcade_blobstore_ops_total (production passes
// metrics.Metrics.BlobstoreOp at boot, where the backend is chosen). The
// blobstore stays metrics-agnostic, matching how the catalog reports its store
// latency through an injected recorder. op is one of get|put|delete|list; ok is
// err == nil (a Get of a missing key is ok=true — absence is an answer, not a
// store failure). A nil s or rec returns s unchanged so callers can wire it
// unconditionally.
func Instrument(s Store, rec func(op string, ok bool)) Store {
	if s == nil || rec == nil {
		return s
	}
	return &instrumentedStore{s: s, rec: rec}
}

type instrumentedStore struct {
	s   Store
	rec func(op string, ok bool)
}

func (i *instrumentedStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	data, ok, err := i.s.Get(ctx, key)
	i.rec("get", err == nil)
	return data, ok, err
}

func (i *instrumentedStore) Put(ctx context.Context, key string, data []byte) error {
	err := i.s.Put(ctx, key, data)
	i.rec("put", err == nil)
	return err
}

func (i *instrumentedStore) Delete(ctx context.Context, key string) error {
	err := i.s.Delete(ctx, key)
	i.rec("delete", err == nil)
	return err
}

func (i *instrumentedStore) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := i.s.List(ctx, prefix)
	i.rec("list", err == nil)
	return keys, err
}
