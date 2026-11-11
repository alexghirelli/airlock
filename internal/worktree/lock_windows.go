//go:build windows

package worktree

import "os"

// No lock on Windows: airlock has not been run there, and worktrees created
// at the same moment may meet git's own race (the error is reported).
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) {}
