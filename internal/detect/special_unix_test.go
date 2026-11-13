//go:build unix

package detect

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSpecialFilesAreNotRead: a manifest that is a FIFO or a symlink to a device
// must not hang the scan (a FIFO blocks on open) or fill memory (/dev/zero never
// ends). Scan must return promptly and see nothing in them.
func TestSpecialFilesAreNotRead(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "composer.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	done := make(chan Findings, 1)
	go func() { done <- Scan(os.DirFS(dir)) }()
	select {
	case f := <-done:
		if !f.Empty() {
			t.Errorf("special files were read: %+v", f)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Scan blocked on a special file")
	}
}
