//go:build !windows

package worktree

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive lock without waiting. The kernel drops it when
// the process ends, however it ends.
func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
