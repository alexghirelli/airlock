package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tasksFolder is a Repo.TasksOf that keeps every worktree's task folder under root.
func tasksFolder(root string) func(string, bool) (string, error) {
	return func(name string, _ bool) (string, error) { return TaskDir(root, name) }
}

// taskFolder is the task folder of the worktree at path, under tasks.
func taskFolder(t *testing.T, tasks, path string) string {
	t.Helper()
	dir, err := TaskDir(tasks, filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"rate-limit", "a", "1st.review", "x_y", "v1.2"} {
		if !ValidName(name) {
			t.Errorf("%q should be a valid name", name)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "../x", "a..b", "-x", ".hidden", "a\x00b", "a b", ".isolated/x"} {
		if ValidName(name) {
			t.Errorf("%q must not be a valid name", name)
		}
	}
}

func TestTaskDirIsOneFolderPerName(t *testing.T) {
	root := t.TempDir()
	dir, err := TaskDir(root, "rate-limit")
	if err != nil || dir != filepath.Join(root, "rate-limit") {
		t.Fatalf("TaskDir: %q, %v", dir, err)
	}
	for _, name := range []string{"", "../escape", "a/b", "x\x00"} {
		if _, err := TaskDir(root, name); err == nil {
			t.Errorf("TaskDir accepted %q", name)
		}
	}
	if _, err := TaskDir("", "ok"); err == nil {
		t.Error("TaskDir without a root must fail")
	}
}

func TestWriteTaskReplacesAtomicallyWithPrivateModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks", "rate-limit")
	if err := writeTask(dir, handoffFile, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeTask(dir, handoffFile, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if got := readTask(dir, handoffFile); got != "second\n" {
		t.Errorf("read %q", got)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("folder mode %v, %v", st.Mode().Perm(), err)
	}
	fi, err := os.Stat(filepath.Join(dir, handoffFile))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("file mode %v, %v", fi.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestWriteTaskRefusesLinkAndDirectoryInTheWay(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "tasks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, focusFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeTask(dir, focusFile, []byte("x")); err == nil {
		t.Fatal("a link in the way was written through")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Errorf("the link's target changed: %q", b)
	}
	if err := os.Mkdir(filepath.Join(dir, reviewFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeTask(dir, reviewFile, []byte("x")); err == nil {
		t.Error("a directory in the way was replaced")
	}
}

func TestReadTaskIgnoresLinksAndTheRestOfTheFolder(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "tasks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, focusFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := readTask(dir, focusFile); got != "" {
		t.Errorf("a link was read: %q", got)
	}
	if got := readTask(dir, reviewFile); got != "" {
		t.Errorf("a missing review read as %q", got)
	}
	if got := readTask("", focusFile); got != "" {
		t.Errorf("no folder read as %q", got)
	}
}

func TestReadTaskCapsTheSize(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", MaxTaskBytes+1)
	if err := os.WriteFile(filepath.Join(dir, reviewFile), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTask(dir, reviewFile); got != "" {
		t.Errorf("a file over the cap was read (%d bytes)", len(got))
	}
	exact := strings.Repeat("y", MaxTaskBytes)
	if err := os.WriteFile(filepath.Join(dir, reviewFile), []byte(exact), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTask(dir, reviewFile); got != exact {
		t.Errorf("a file at the cap was not read (%d bytes)", len(got))
	}
	if err := writeTask(dir, handoffFile, []byte(big)); err == nil {
		t.Error("a write over the cap was accepted")
	}
}

func TestPrepareDirRefusesALinkInPlaceOfTheFolder(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "tasks")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeTask(link, handoffFile, []byte("x")); err == nil {
		t.Error("a task was written into a folder reached through a link")
	}
	if _, err := os.Lstat(filepath.Join(real, handoffFile)); err == nil {
		t.Error("the link's target received a task")
	}
}

func TestRemoveTaskLeavesMissingFilesAlone(t *testing.T) {
	dir := t.TempDir()
	if err := removeTask(dir, handoffFile); err != nil {
		t.Errorf("removing a missing file: %v", err)
	}
	if err := removeTask("", handoffFile); err == nil {
		t.Error("removing with no folder must fail")
	}
}
