package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// An attach lease says "an airlock process on this machine has a Herdr client
// attached to this sandbox". Inside the sandbox nothing tells a live terminal
// from a closed one: Docker keeps the exec's pty open, so the Herdr client
// outlives the window it was shown in. The airlock process that attached is the
// one that knows, and it dies with its terminal.

func leaseDir() string { return filepath.Join(StateDir(), "attached") }

func leasePath(container string, pid int) string {
	return filepath.Join(leaseDir(), container+"."+strconv.Itoa(pid))
}

// AcquireAttach records that this process is attached to the sandbox; call the
// returned function when it detaches. Failing to write the lease only means
// other commands will attach their own client instead of staying quiet.
func AcquireAttach(container string) (release func()) {
	path := leasePath(container, os.Getpid())
	if err := os.MkdirAll(leaseDir(), 0o755); err != nil {
		return func() {}
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// AttachedElsewhere reports whether another live airlock process has a Herdr
// client attached to the sandbox. Leases of processes that are gone (a closed
// terminal) are removed on the way.
func AttachedElsewhere(container string) bool {
	return attachedElsewhere(container, os.Getpid())
}

func attachedElsewhere(container string, self int) bool {
	entries, _ := os.ReadDir(leaseDir())
	found := false
	for _, e := range entries {
		name := e.Name()
		dot := strings.LastIndex(name, ".")
		if dot < 0 {
			continue
		}
		pid, err := strconv.Atoi(name[dot+1:])
		if err != nil || pid == self {
			continue
		}
		if processAlive(pid) {
			found = found || name[:dot] == container
		} else {
			_ = os.Remove(filepath.Join(leaseDir(), name)) // every sandbox's, not only this one's
		}
	}
	return found
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" { // FindProcess already failed for a process that is gone
		_ = p.Release()
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}
