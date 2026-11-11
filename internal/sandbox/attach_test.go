package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestAttachLeases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a child process to stand for another airlock")
	}
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	const c = "airlock-demo-1234"

	if AttachedElsewhere(c) {
		t.Fatal("nothing is attached yet")
	}

	// Our own lease never counts: the question is about other terminals.
	release := AcquireAttach(c)
	if AttachedElsewhere(c) {
		t.Error("a process counted its own lease")
	}
	release()
	if _, err := os.Stat(leasePath(c, os.Getpid())); err == nil {
		t.Error("the lease survived its release")
	}

	// Another live process holding a lease for the sandbox: attached.
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	live := leasePath(c, sleeper.Process.Pid)
	os.MkdirAll(leaseDir(), 0o755)
	os.WriteFile(live, nil, 0o644)
	if !AttachedElsewhere(c) {
		t.Error("a live process holds a lease but nobody is attached")
	}
	// ... but only for its own sandbox.
	if AttachedElsewhere("airlock-other-5678") {
		t.Error("a lease of one sandbox counted for another")
	}

	// A terminal that was closed killed its airlock: the lease is dead and goes away.
	_ = sleeper.Process.Kill()
	_ = sleeper.Wait()
	if AttachedElsewhere(c) {
		t.Error("a dead process still counts as attached")
	}
	if _, err := os.Stat(live); err == nil {
		t.Error("the stale lease was not cleaned up")
	}

	// Junk in the folder is ignored.
	os.WriteFile(filepath.Join(leaseDir(), c+".notapid"), nil, 0o644)
	os.WriteFile(filepath.Join(leaseDir(), c+"."+strconv.Itoa(1<<30)), nil, 0o644) // no such process
	if AttachedElsewhere(c) {
		t.Error("junk counted as a lease")
	}
}
