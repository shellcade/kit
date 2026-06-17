---
"kit": patch
---

host: detect CPU steal at callback-deadline kills (detection only)

The per-callback kill switch is wall-clock only, and a true fuel/instruction
budget is impossible on this stack (wazero v1.12.0 / extism v1.7.1 expose no
fuel/epoch/gas metering API). So a CPU-steal-throttled VM can blow the deadline
on a guest running pure, well-behaved guest code, and that kill is booked as a
fault that can quarantine a healthy game.

This change MEASURES host-stolen CPU at the kill site and records it alongside
the existing deadline metric — detection only, no behavior change:

- A failure-tolerant, build-tagged steal sampler reads ONLY the /proc/stat
  aggregate hypervisor-STEAL field (Linux; a no-op ok=false stub elsewhere),
  sampled at callback BOUNDARIES, never per-instruction. cgroup throttling is
  deliberately NOT read: steal blames the host (the eventual exonerate case),
  whereas cgroup throttle blames the guest (a runaway you must not exonerate).
- A new NON-BREAKING optional `StealMetrics` extension interface is recorded via
  a type assertion at the wall-clock-kill site, so existing Metrics implementers
  compile and run unchanged. fault(), quarantine, and End() are untouched; the
  existing GameCallbackDeadline record is never replaced or suppressed.
