//go:build windows

package game

import "os"

// watchResize on Windows returns a channel that never fires: there is no
// SIGWINCH equivalent, so the dev runner simply doesn't re-letterbox on
// resize. Everything else (raw mode, input, rendering) works; the runner
// re-measures on the next session anyway.
func watchResize() (winch <-chan os.Signal, stop func()) {
	return make(chan os.Signal), func() {}
}
