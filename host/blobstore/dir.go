package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is a file-backed Store rooted at a directory: each slash-separated key
// maps to a file under root, with key segments becoming subdirectories. It
// exists for dev mode — set SHELLCADE_BLOB_DIR so hibernation snapshots (and
// the sideloaded catalog) survive a `serve` restart, which the in-memory
// double cannot do. Production still uses S3; this is never wired in prod.
type Dir struct {
	root string
}

// NewDir returns a Dir store rooted at root, creating it if needed.
func NewDir(root string) (*Dir, error) {
	if root == "" {
		return nil, errors.New("blobstore: dir: empty root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("blobstore: dir: mkdir root: %w", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("blobstore: dir: abs root: %w", err)
	}
	return &Dir{root: abs}, nil
}

// path maps a slash-separated key to an absolute file path under root, and
// guards against keys that would escape root (e.g. "../etc/passwd").
func (d *Dir) path(key string) (string, error) {
	p := filepath.Join(d.root, filepath.FromSlash(key))
	if p != d.root && !strings.HasPrefix(p, d.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("blobstore: dir: key escapes root: %q", key)
	}
	return p, nil
}

func (d *Dir) Get(ctx context.Context, key string) ([]byte, bool, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("blobstore: dir: get %s: %w", key, err)
	}
	return data, true, nil
}

func (d *Dir) Put(ctx context.Context, key string, data []byte) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("blobstore: dir: put mkdir %s: %w", key, err)
	}
	// Write to a temp file then rename so a crash mid-write never leaves a
	// truncated snapshot that resume would choke on.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return fmt.Errorf("blobstore: dir: put temp %s: %w", key, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("blobstore: dir: put write %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("blobstore: dir: put close %s: %w", key, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("blobstore: dir: put rename %s: %w", key, err)
	}
	return nil
}

func (d *Dir) Delete(ctx context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blobstore: dir: delete %s: %w", key, err)
	}
	return nil
}

func (d *Dir) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(d.root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(d.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		// Skip in-flight temp files from interrupted Put calls.
		if strings.HasPrefix(filepath.Base(p), ".tmp-") {
			return nil
		}
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("blobstore: dir: list %s: %w", prefix, err)
	}
	sort.Strings(keys)
	return keys, nil
}
