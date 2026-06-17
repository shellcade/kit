//go:build !linux

package gameabi

// readStealJiffiesLinux is the no-op stub on non-Linux hosts: there is no
// /proc/stat hypervisor-steal accounting to read, so it always reports no steal
// info (ok=false) and the caller records no steal metric — exactly the current
// behavior. Production runs on Linux; this keeps the package compiling and the
// cross-platform stubbed tests honest on darwin/etc.
func readStealJiffiesLinux() (steal uint64, ok bool) {
	return 0, false
}
