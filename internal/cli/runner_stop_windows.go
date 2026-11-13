//go:build windows

package cli

import "os"

// stopProcess asks a process to stop. Windows cannot send SIGTERM to a process,
// so the interrupt is sent; if the process ignores it, WaitDelay kills it.
func stopProcess(p *os.Process) error {
	return p.Signal(os.Interrupt)
}
