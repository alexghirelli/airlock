//go:build unix

package worktree

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO in the task folder must neither block a read nor be read.
func TestReadTaskNeverBlocksOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, focusFile), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan string, 1)
	go func() { done <- readTask(dir, focusFile) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("a FIFO was read: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO in the task folder blocked")
	}
}

// Attack 1 with a FIFO: a FIFO the agent puts in the admin directory must not
// hang `wt ls` or `summary`, which read the task folder and never that directory.
func TestAttackFIFOInTheAdminDirectoryDoesNotHangTheHost(t *testing.T) {
	admin, path := agentWorktree(t)
	if err := syscall.Mkfifo(filepath.Join(admin, "airlock-review.md"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(admin, "airlock-focus.md"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	dir, err := TaskDir(t.TempDir(), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- Focus(dir) + Brief(dir) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("a FIFO in the admin directory became a task: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host blocked on a FIFO in the admin directory")
	}
}
