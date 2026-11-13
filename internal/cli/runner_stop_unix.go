//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// stopProcess asks a process to stop, as a terminal's interrupt would not reach a
// container's process anyway: docker gets SIGTERM and forwards it.
func stopProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
