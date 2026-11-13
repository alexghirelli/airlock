package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alexghirelli/airlock/internal/worktree"
)

// TasksMount is where the host's task folders appear inside a sandbox: the
// folder of each worktree the sandbox serves, read-only (see plan.go).
const TasksMount = "/run/airlock/tasks"

// WorktreeTasks is the host folder of the task files of the named worktree of
// the repository at repoRoot: <state>/tasks/<slug>/<name>, where the slug names
// the sandbox that serves the worktree. A worktree in the shared folder is
// served by the repository's sandbox; one in the isolated folder has a sandbox
// of its own, so its slug comes from its path, as Project.Settle has it.
//
// It is computed from what the host knows (the repository, the folders and the
// name), never from the worktree's .git file, which the agent can rewrite.
func WorktreeTasks(repoRoot, worktreesDir, name string, isolated bool) (string, error) {
	if !worktree.ValidName(name) {
		return "", fmt.Errorf("invalid worktree name %q", name)
	}
	key := slug(repoRoot)
	if isolated {
		wt := filepath.Join(repoRoot, filepath.FromSlash(strings.Trim(filepath.ToSlash(worktreesDir), "/")), worktree.IsolatedDir, name)
		key = worktreeSlug(repoRoot, wt)
	}
	return worktree.TaskDir(filepath.Join(StateDir(), "tasks", key), name)
}

// taskMount is the host folder that a sandbox mounts for its task files, and
// where it appears inside. The project's sandbox gets the folder of every
// worktree that shares it. A worktree with a sandbox of its own (isolated) gets
// only its own folder, so it cannot read the others' tasks.
func taskMount(p *Project, worktreesDir string) (src, target string, err error) {
	if !p.IsWorktree() {
		return filepath.Join(StateDir(), "tasks", p.Slug), TasksMount, nil
	}
	name := filepath.Base(p.Root)
	src, err = WorktreeTasks(p.Main, worktreesDir, name, true)
	return src, TasksMount + "/" + name, err
}
