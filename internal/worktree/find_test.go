package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// Find accepts a real folder with a .git file only. The worktrees folder is the
// agent's to write: a link there, or a folder without the file, is not a worktree.
func TestFindRefusesLinksAndFoldersWithoutAGitFile(t *testing.T) {
	root := t.TempDir()
	dirs := filepath.Join(root, ".worktrees")
	if err := os.MkdirAll(dirs, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dirs, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, ".git"), []byte("gitdir: /x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dirs, "nogit"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(dirs, "gitdir")
	if err := os.MkdirAll(filepath.Join(gitDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(dirs, "evil")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../..", filepath.Join(dirs, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dirs, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, ".git"), filepath.Join(dirs, "gitlink")); err != nil {
		t.Fatal(err)
	}
	r := &Repo{Root: root, Dir: ".worktrees"}

	if p, _, ok := r.Find("real"); !ok || p != real {
		t.Errorf("real worktree: %q, %v", p, ok)
	}
	for _, name := range []string{"nogit", "gitdir", "evil", "up", "linked", "missing"} {
		if p, _, ok := r.Find(name); ok {
			t.Errorf("Find(%q) = %q; want not found", name, p)
		}
	}
	// A .git link inside a real folder is not a worktree either.
	if err := os.Remove(filepath.Join(real, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(real, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := r.Find("real"); ok {
		t.Error("a .git link is accepted")
	}
}
