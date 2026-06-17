package gameabi

// CPU-steal DETECTION (detection only — no behavior change).
//
// Why this exists: the per-callback kill switch is WALL-CLOCK only. A true
// fuel/instruction budget is impossible on this stack — wazero v1.12.0 and
// extism v1.7.1 expose no fuel/epoch/gas metering API to bound a guest by work
// done rather than time elapsed — so a CPU-steal-throttled VM (a noisy neighbor
// on oversubscribed hardware) can blow the wall-clock deadline on a guest that
// is running PURE, well-behaved guest code, and that kill is booked as a fault
// that can quarantine a healthy game. (The slow-shared-Postgres analogue is
// already carved out via hostIOKill.)
//
// The only available move is to MEASURE host-stolen CPU at kill time. This file
// adds that measurement ALONGSIDE the existing GameCallbackDeadline record. It
// does NOT change fault(), quarantine, End(), or any kill/condemn decision: no
// exoneration is wired yet. See steal_linux.go for the single signal (/proc/stat
// hypervisor steal), its blame direction, and its coarseness caveats.

// stealReader returns the cumulative host-stolen CPU time in jiffies and whether
// that figure is available. It is an INJECTABLE package var (defaulting to the
// real per-GOOS reader) so tests can stub steal without a real /proc/stat. The
// host reads it only at CALLBACK BOUNDARIES (before invoking the guest, and at
// the kill/return) — never on a hot per-instruction path.
var stealReader = readStealJiffiesLinux

// StealMetrics is a NON-BREAKING optional extension of the Metrics surface. It
// is deliberately NOT folded into the Metrics interface: doing so would break
// every existing Metrics implementer (notably the platform's) on the next kit
// bump. The kill site type-asserts the configured Metrics to StealMetrics and
// records ONLY if the implementer opts in, so older implementers compile and
// run unchanged while the seam stays dormant until the platform implements it.
type StealMetrics interface {
	// GameCallbackStealDeadline records one wall-clock callback-deadline kill
	// during which host-stolen CPU advanced — i.e. the VM was being stolen from
	// across the killed callback's window. Recorded ALONGSIDE (never instead of)
	// GameCallbackDeadline. A correlation signal for the future exonerate case,
	// not a kill/quarantine decision.
	GameCallbackStealDeadline(slug, callback string)
}
