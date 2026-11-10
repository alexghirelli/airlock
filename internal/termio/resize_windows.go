//go:build windows

package termio

import "time"

// WatchResize polls the console size: Windows has no SIGWINCH.
func (t *Terminal) WatchResize(fn func(w, h int)) (stop func()) {
	done := make(chan struct{})
	lw, lh := t.Size()
	fn(lw, lh)
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if w, h := t.Size(); w != lw || h != lh {
					lw, lh = w, h
					fn(w, h)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
