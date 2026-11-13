package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/backend"
)

// mkIsolatedWorktree makes a linked worktree in the isolated folder of the
// repository at root, and returns it as the sandbox sees it.
func mkIsolatedWorktree(t *testing.T, root, name string) *Project {
	t.Helper()
	wt := filepath.Join(root, ".worktrees", ".isolated", name)
	admin := filepath.Join(root, ".git", "worktrees", name)
	for _, d := range []string{wt, admin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkWorktree(t, wt, admin) // the file and the admin directory's back-references, as git writes them
	p, err := Resolve(wt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// withoutBind is a copy of spec without the bind to target.
func withoutBind(spec backend.Spec, target string) backend.Spec {
	var kept []backend.Bind
	for _, b := range spec.Binds {
		if b.Target != target {
			kept = append(kept, b)
		}
	}
	spec.Binds = kept
	return spec
}

// The project's sandbox gets the task folder of every worktree that shares it,
// read-only, and the folder is not in the worktree or its git directory.
func TestProjectSandboxMountsItsTaskFolderReadOnly(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	main, _ := Resolve(root)
	plan := testPlan(t, "", main, nil)

	b := findBind(plan, TasksMount)
	if b == nil || !b.ReadOnly {
		t.Fatalf("the task folder must be mounted read-only at %s: %+v", TasksMount, b)
	}
	if want := filepath.Join(StateDir(), "tasks", main.Slug); b.Source != want || plan.TasksDir != want {
		t.Errorf("task folder %q (plan %q), want %q", b.Source, plan.TasksDir, want)
	}
	if strings.HasPrefix(b.Source, root) {
		t.Errorf("the task folder %s is inside the project", b.Source)
	}
}

// Changing the mount changes the policy hash, so a sandbox made before it is recreated.
func TestTaskMountIsPartOfThePolicy(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	main, _ := Resolve(root)
	plan := testPlan(t, "", main, nil)
	if policyHash(plan.Spec, nil) != plan.Policy {
		t.Fatal("test assumption: the policy is the hash of the spec without masks")
	}
	if policyHash(withoutBind(plan.Spec, TasksMount), nil) == plan.Policy {
		t.Error("the task mount does not change the policy hash")
	}
}

// A worktree in the shared folder is served by the project's sandbox, and the
// folder the CLI writes its task files to is one of the folders that sandbox mounts.
func TestSharedWorktreeTaskFolderIsInTheMountedRoot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	wt := mkWorktree(t, root, "review-1").Settle(".worktrees")
	plan := testPlan(t, "", wt, nil)

	want, err := WorktreeTasks(root, ".worktrees", "review-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(want) != plan.TasksDir {
		t.Errorf("the worktree's folder %q is not in the mounted %q", want, plan.TasksDir)
	}
	if b := findBind(plan, TasksMount); b == nil || !b.ReadOnly || b.Source != plan.TasksDir {
		t.Errorf("shared worktree: task mount %+v", b)
	}
}

// An isolated worktree sees only its own task folder, not its siblings' and not
// the folder of the project's sandbox.
func TestIsolatedWorktreeMountsOnlyItsOwnTaskFolder(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	wt := mkIsolatedWorktree(t, root, "feature")
	mkIsolatedWorktree(t, root, "sibling")
	plan := testPlan(t, "", wt, nil)

	want, err := WorktreeTasks(root, ".worktrees", "feature", true)
	if err != nil {
		t.Fatal(err)
	}
	own := findBind(plan, TasksMount+"/feature")
	if own == nil || !own.ReadOnly || own.Source != want || plan.TasksDir != want {
		t.Fatalf("isolated worktree: its task folder %+v (plan %q), want %q", own, plan.TasksDir, want)
	}
	if findBind(plan, TasksMount) != nil {
		t.Error("an isolated worktree mounts the whole task folder")
	}
	tasksRoot := filepath.Join(StateDir(), "tasks")
	for _, b := range plan.Spec.Binds {
		if strings.HasPrefix(b.Source, tasksRoot) && b.Source != want {
			t.Errorf("an isolated worktree mounts another task folder: %+v", b)
		}
	}
	if filepath.Base(want) != "feature" {
		t.Errorf("task folder %q is not named after the worktree", want)
	}
}

// Each isolated worktree has a folder of its own.
func TestIsolatedTaskFoldersAreDistinct(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	a, err := WorktreeTasks(root, ".worktrees", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := WorktreeTasks(root, ".worktrees", "b", true)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(a) == filepath.Dir(b) {
		t.Errorf("two isolated worktrees share the folder %q", filepath.Dir(a))
	}
}

func TestWorktreeTasksRefusesBadNames(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := realTemp(t)
	for _, name := range []string{"", "..", "../x", "a/b", "a\x00b"} {
		for _, iso := range []bool{false, true} {
			if got, err := WorktreeTasks(root, ".worktrees", name, iso); err == nil {
				t.Errorf("name %q (isolated %v) gave %q", name, iso, got)
			}
		}
	}
}
