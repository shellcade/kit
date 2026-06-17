//go:build linux

package gameabi

import (
	"os"
	"strconv"
	"strings"
)

// procStatPath is the file the steal reader parses. A package var (not a const)
// so a //go:build linux test can point it at a synthetic fixture without a real
// /proc — the production default is the kernel's live aggregate CPU accounting.
var procStatPath = "/proc/stat"

// readStealJiffies returns the cumulative hypervisor-STEAL time, in USER_HZ
// jiffies (~10ms each), that the kernel has accounted across ALL CPUs since
// boot — the 8th field of the aggregate "cpu " line in /proc/stat:
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
//	     1    2    3      4    5      6   7       8     ...
//
// "Steal" is wall-clock time during which a runnable vCPU was NOT scheduled
// because the HYPERVISOR ran someone else — i.e. the host took CPU away from
// us. That is the ONLY signal this reader exposes, by deliberate choice:
//
//   - Steal blames the HOST (a noisy-neighbor / oversubscribed VM), so a
//     deadline kill that coincides with advancing steal is the future
//     EXONERATE case — a well-behaved guest punished for the platform's
//     scheduling, not a runaway. (No exoneration is wired yet; this is
//     detection only.)
//   - cgroup throttled_usec (CPU quota throttling) blames US: the guest
//     exceeded its OWN cpu.max quota, which is exactly the runaway you must
//     NOT exonerate. It is therefore intentionally NOT read and NOT OR'd in.
//
// Coarseness caveats, by construction:
//   - HOST-WIDE: the aggregate "cpu " line counts steal across every vCPU and
//     every tenant process on this VM, not steal attributable to this room's
//     goroutine. It can only ever say "the VM was being stolen from around the
//     time this callback ran", never "this callback's CPU was stolen".
//   - JIFFY RESOLUTION: USER_HZ is typically 100Hz, so the counter advances in
//     ~10ms steps — comparable to the default 100ms callback deadline. A short
//     callback can be wholly stolen yet straddle no jiffy tick (false negative);
//     a long one can tick over a jiffy boundary on unrelated steal (false
//     positive). Sampling at callback boundaries (not per-instruction) keeps it
//     cheap at the cost of this granularity.
//
// ok is false on ANY read or parse failure (missing file, short line,
// non-numeric field): the caller treats "no steal info" as the current
// behavior, recording no steal metric. A guest cannot influence this file.
func readStealJiffiesLinux() (steal uint64, ok bool) {
	b, err := os.ReadFile(procStatPath)
	if err != nil {
		return 0, false
	}
	// The aggregate line is the first line and starts with "cpu " (note the
	// trailing space — "cpu0", "cpu1", ... are the per-core lines we skip).
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line) // ["cpu", user, nice, system, idle, iowait, irq, softirq, steal, ...]
		// fields[0] == "cpu"; steal is the 8th value AFTER the label => index 8.
		if len(fields) <= 8 {
			return 0, false
		}
		v, perr := strconv.ParseUint(fields[8], 10, 64)
		if perr != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
