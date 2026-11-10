//go:build !windows

package termio

import (
	"os"
	"os/signal"
	"syscall"
)

// WatchResize calls fn with the new size on every terminal resize, and once
// right away. The returned function stops watching.
func (t *Terminal) WatchResize(fn func(w, h int)) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				fn(t.Size())
			case <-done:
				return
			}
		}
	}()
	fn(t.Size())
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
