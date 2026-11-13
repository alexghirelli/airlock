package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeWorktree is a linked worktree as far as AdminDir is concerned: a folder
// whose .git file points at an admin directory inside a repository's worktrees
// folder. It returns the repository root, the worktree and its admin directory.
func fakeWorktree(t *testing.T) (root, path, admin string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "repo")
	path = filepath.Join(base, "rate-limit")
	admin = filepath.Join(root, ".git", "worktrees", "rate-limit")
	for _, d := range []string{path, admin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, path, admin
}

func TestWriteHandoffReplacesAtomically(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	target := filepath.Join(admin, handoffFile)
	if err := WriteHandoff(root, path, "first\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(root, path, "second\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "second\n" {
		t.Errorf("briefing = %q, %v; want the second one", b, err)
	}
	fi, err := os.Stat(target)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, %v; want 0644", fi.Mode().Perm(), err)
	}
	// Only the briefing is left in the admin directory: no temporary files.
	entries, err := os.ReadDir(admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != handoffFile {
			t.Errorf("unexpected entry %q left in the admin directory", e.Name())
		}
	}
	// Nothing lands in the working tree.
	if _, err := os.Stat(filepath.Join(path, handoffFile)); !os.IsNotExist(err) {
		t.Errorf("briefing written into the working tree: %v", err)
	}
}

func TestWriteHandoffRefusesSymlink(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(admin, handoffFile)); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if err := WriteHandoff(root, path, "new\n"); err == nil {
		t.Fatal("a symlink was not refused")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep me\n" {
		t.Errorf("the file behind the symlink was written: %q", b)
	}
	if err := ClearHandoff(root, path); err != nil {
		t.Errorf("clearing a symlink: %v", err)
	}
}

func TestWriteHandoffRefusesDirectory(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	if err := os.Mkdir(filepath.Join(admin, handoffFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(root, path, "x\n"); err == nil {
		t.Error("a directory in the way was not refused")
	}
}

func TestWriteHandoffNeedsALinkedWorktree(t *testing.T) {
	root, _, _ := fakeWorktree(t)
	if err := WriteHandoff(root, t.TempDir(), "x\n"); err == nil {
		t.Error("a folder that is not a worktree was accepted")
	}
	if err := ClearHandoff(root, t.TempDir()); err == nil {
		t.Error("ClearHandoff accepted a folder that is not a worktree")
	}
}

// The .git file of a worktree is written by its agent: pointing it at a
// directory outside the repository's worktrees must stop every write there.
func TestCheckedAdminDirRefusesPointerOutside(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+outside+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckedAdminDir(root, path); err == nil {
		t.Error("an admin directory outside the worktrees was accepted")
	}
	if err := WriteHandoff(root, path, "x\n"); err == nil {
		t.Error("the briefing was written outside the repository")
	}
	if err := SetFocus(root, path, "x"); err == nil {
		t.Error("the focus was written outside the repository")
	}
	if _, err := os.Stat(filepath.Join(outside, handoffFile)); err == nil {
		t.Error("a file appeared outside the repository")
	}
	// The real admin directory still works, once the pointer is right again.
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(root, path, "x\n"); err != nil {
		t.Errorf("a correct pointer refused: %v", err)
	}
}

// An entry under .git/worktrees replaced by a link to somewhere else.
func TestCheckedAdminDirRefusesSymlinkedAdminDir(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	elsewhere := t.TempDir()
	if err := os.RemoveAll(admin); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, admin); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if err := WriteHandoff(root, path, "x\n"); err == nil {
		t.Error("a symlinked admin directory was accepted")
	}
	if err := SetFocus(root, path, "x"); err == nil {
		t.Error("focus written through a symlinked admin directory")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, handoffFile)); err == nil {
		t.Error("the briefing was written through the symlink")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, focusFile)); err == nil {
		t.Error("the focus was written through the symlink")
	}
}

// The review brief goes through the same check: a pointer that leads outside
// the repository's worktrees must not get a file written there.
func TestBriefRefusesPointerOutside(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	outside := t.TempDir()
	pointer := filepath.Join(path, ".git")
	if err := os.WriteFile(pointer, []byte("gitdir: "+outside+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Repo{Root: root}
	if err := r.writeBrief(path, Change{}, "", "abc123", "review/1", ""); err == nil {
		t.Fatal("a brief was written through a pointer outside the repository")
	}
	if _, err := os.Stat(filepath.Join(outside, briefFile)); err == nil {
		t.Error("the review brief landed outside the repository")
	}
	// With the pointer back, the brief is written in the admin directory.
	if err := os.WriteFile(pointer, []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.writeBrief(path, Change{}, "", "abc123", "review/1", ""); err != nil {
		t.Fatalf("correct pointer refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(admin, briefFile)); err != nil {
		t.Errorf("brief not written in the admin directory: %v", err)
	}
}

func TestClearHandoff(t *testing.T) {
	root, path, admin := fakeWorktree(t)
	// Nothing to clear is fine: --reset on a worktree that was never handed over.
	if err := ClearHandoff(root, path); err != nil {
		t.Errorf("clear without a briefing: %v", err)
	}
	if err := WriteHandoff(root, path, "x\n"); err != nil {
		t.Fatal(err)
	}
	if err := ClearHandoff(root, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(admin, handoffFile)); !os.IsNotExist(err) {
		t.Errorf("briefing still there after clear: %v", err)
	}
	// It leaves the focus alone.
	if err := SetFocus(root, path, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(root, path, "x\n"); err != nil {
		t.Fatal(err)
	}
	if err := ClearHandoff(root, path); err != nil {
		t.Fatal(err)
	}
	if Focus(path) != "keep" {
		t.Errorf("focus lost by clearing the handoff: %q", Focus(path))
	}
}
