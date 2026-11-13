package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexghirelli/airlock/internal/worktree"
)

// `wt rm` finds the worktree's task folder before it removes the worktree, then
// deletes the folder. This runs the same steps on a folder that exists.
func TestRemovingAWorktreeDeletesItsTaskFolder(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	tasks := tasksOf(root, ".worktrees")

	for _, isolated := range []bool{false, true} {
		dir, err := tasks("rate-limit", isolated)
		if err != nil {
			t.Fatal(err)
		}
		if err := worktree.SetFocus(dir, "focus"); err != nil {
			t.Fatal(err)
		}
		if err := worktree.WriteHandoff(dir, "handoff\n"); err != nil {
			t.Fatal(err)
		}
		if err := worktree.RemoveTasks(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("isolated %v: task folder %s still there: %v", isolated, dir, err)
		}
	}
}

// The folders of a shared worktree and of an isolated one with the same name
// are different: the isolated one is served by its own sandbox.
func TestSharedAndIsolatedWorktreesHaveDifferentTaskFolders(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	tasks := tasksOf(root, ".worktrees")
	shared, err := tasks("review-1", false)
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := tasks("review-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if shared == isolated {
		t.Errorf("both are %s", shared)
	}
	if filepath.Base(shared) != "review-1" || filepath.Base(isolated) != "review-1" {
		t.Errorf("folders %q and %q are not named after the worktree", shared, isolated)
	}
}
