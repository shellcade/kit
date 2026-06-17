//go:build linux

package gameabi

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadStealJiffiesLinux parses the STEAL field (the 8th value after the
// "cpu" label) from a synthetic /proc/stat fed via the injectable procStatPath,
// and pins the failure-tolerant contract: ANY read/parse problem => ok=false.
func TestReadStealJiffiesLinux(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "stat")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return p
	}
	setPath := func(t *testing.T, p string) {
		t.Helper()
		prev := procStatPath
		procStatPath = p
		t.Cleanup(func() { procStatPath = prev })
	}

	for _, tc := range []struct {
		name      string
		content   string
		wantSteal uint64
		wantOK    bool
	}{
		{
			name: "real-shaped line parses the 8th field",
			//      user nice system idle    iowait irq softirq STEAL guest guest_nice
			content:   "cpu  100  20   30     40000   5      6   7       4242  9     10\ncpu0 1 2 3 4 5 6 7 8\nintr 0\n",
			wantSteal: 4242,
			wantOK:    true,
		},
		{
			name:      "zero steal (unstolen VM) is a valid reading",
			content:   "cpu  1 2 3 4 5 6 7 0 9 10\n",
			wantSteal: 0,
			wantOK:    true,
		},
		{
			name:    "short line (no steal field) => ok=false",
			content: "cpu  1 2 3 4\n",
			wantOK:  false,
		},
		{
			name:    "non-numeric steal field => ok=false",
			content: "cpu  1 2 3 4 5 6 7 xx 9\n",
			wantOK:  false,
		},
		{
			name:    "no aggregate cpu line => ok=false",
			content: "cpu0 1 2 3 4 5 6 7 8\nintr 0\n",
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setPath(t, write(t, tc.content))
			got, ok := readStealJiffiesLinux()
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.wantSteal {
				t.Fatalf("steal = %d, want %d", got, tc.wantSteal)
			}
		})
	}

	// Missing file => ok=false (failure-tolerant, never panics).
	setPath(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if _, ok := readStealJiffiesLinux(); ok {
		t.Fatal("missing /proc/stat reported ok=true, want false")
	}
}

// TestReadStealJiffiesMonotonic asserts the LIVE /proc/stat reader is available
// and non-decreasing on a real Linux host: steal is a cumulative-since-boot
// counter, so a later read can never be below an earlier one.
func TestReadStealJiffiesMonotonic(t *testing.T) {
	// Default path (real /proc/stat) — procStatPath untouched.
	a, okA := readStealJiffiesLinux()
	if !okA {
		t.Skip("live /proc/stat steal field unavailable on this host")
	}
	b, okB := readStealJiffiesLinux()
	if !okB {
		t.Fatal("second live read failed after a successful first read")
	}
	if b < a {
		t.Fatalf("steal counter went backwards: %d -> %d (must be monotonic)", a, b)
	}
}
