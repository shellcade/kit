//go:build !wasip1 && !tinygo.wasm && !windows

package game

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize delivers terminal-resize notifications (SIGWINCH) to the dev
// runner's loop; stop releases the signal registration.
func watchResize() (winch <-chan os.Signal, stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}
