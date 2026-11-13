package imagebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock"
)

// wrapperEnv is an environment for a wrapper run: git reads no global or system
// configuration, and nothing is inherited from a git hook or a rebase.
func wrapperEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	}, extra...)
}

// gitIn runs git in dir with the wrapper environment, and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org"}, args...)...)
	cmd.Dir, cmd.Env = dir, wrapperEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// handoffWorktree is a linked worktree named "rate-limit" of a repository, and
// the task folder the host would mount for it: its focus and its handoff
// briefing (as `airlock wt focus` and `airlock wt handoff` write them). It
// returns the worktree, the repository's git directory and the task folder root.
func handoffWorktree(t *testing.T) (wt, gitDir, tasks string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main", repo)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt = filepath.Join(base, "rate-limit")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "rate-limit", wt)

	tasks = filepath.Join(base, "tasks")
	folder := filepath.Join(tasks, "rate-limit")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"focus.md":   "FOCUS-MARKER\n",
		"handoff.md": "HANDOFF-MARKER\n",
	} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return wt, filepath.Join(repo, ".git"), tasks
}

// runWrapper runs an image script from the embedded assets, in dir, with args.
// The folder the script reads the task files from is the one the sandbox
// mounts; in the test it is tasks.
func runWrapper(t *testing.T, script, dir, tasks string, env []string, args ...string) string {
	t.Helper()
	src, err := airlock.Assets.ReadFile("image/" + script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "/run/airlock/tasks") {
		t.Fatalf("%s does not read the mounted task folder", script)
	}
	text = strings.ReplaceAll(text, "/run/airlock/tasks", tasks)
	sp := filepath.Join(t.TempDir(), script)
	if err := os.WriteFile(sp, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{sp}, args...)...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", script, args, err, out)
	}
	return string(out)
}

// The handoff briefing is appended on every start, a resumed conversation too,
// until it is reset or replaced (like the focus).
func TestPiWrapperHandoffOnEveryStart(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is needed to run the wrapper: " + err.Error())
	}
	wt, _, tasks := handoffWorktree(t)
	env := wrapperEnv(t, "AIRLOCK_PI_ARGS_ONLY=1")
	cases := []struct {
		args        []string
		wantHandoff bool
	}{
		{nil, true},
		{[]string{"--model", "x"}, true},
		{[]string{"--continue"}, true},
		{[]string{"--model", "x", "--continue"}, true},
	}
	for _, c := range cases {
		out := runWrapper(t, "pi-wrapper.sh", wt, tasks, env, c.args...)
		if got := strings.Contains(out, "handoff.md"); got != c.wantHandoff {
			t.Errorf("pi %v: handoff included = %v, want %v:\n%s", c.args, got, c.wantHandoff, out)
		}
		if !strings.Contains(out, "focus.md") {
			t.Errorf("pi %v: the focus is no longer passed:\n%s", c.args, out)
		}
	}
	// Without a briefing file there is nothing to add, fresh or not.
	if err := os.Remove(filepath.Join(tasks, "rate-limit", "handoff.md")); err != nil {
		t.Fatal(err)
	}
	if out := runWrapper(t, "pi-wrapper.sh", wt, tasks, env); strings.Contains(out, "handoff.md") {
		t.Errorf("no briefing file, yet it is passed:\n%s", out)
	}
}

func TestClaudeWrapperHandoffOnEveryStart(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is needed to run the wrapper: " + err.Error())
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is needed by claude-wrapper.sh to read the manifest: " + err.Error())
	}
	wt, _, tasks := handoffWorktree(t)
	env := wrapperEnv(t, "AIRLOCK_AGENT_ARGS_ONLY=1")
	fresh := runWrapper(t, "claude-wrapper.sh", wt, tasks, env)
	if !strings.Contains(fresh, "HANDOFF-MARKER") || !strings.Contains(fresh, "FOCUS-MARKER") {
		t.Errorf("fresh claude does not get the briefing and the focus:\n%s", fresh)
	}
	resumed := runWrapper(t, "claude-wrapper.sh", wt, tasks, env, "--continue")
	if !strings.Contains(resumed, "HANDOFF-MARKER") {
		t.Errorf("a resumed claude lost the briefing:\n%s", resumed)
	}
	if !strings.Contains(resumed, "FOCUS-MARKER") {
		t.Errorf("a resumed claude lost the focus:\n%s", resumed)
	}
}

// The task files are read from the mounted folder only. A file of the same name
// in the worktree's git directory, which the agent can write, is never passed.
func TestWrappersIgnoreTaskFilesInTheGitDirectory(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is needed to run the wrapper: " + err.Error())
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is needed by claude-wrapper.sh to read the manifest: " + err.Error())
	}
	wt, gitDir, tasks := handoffWorktree(t)
	// The worktree's git directory, as an agent with write access would leave it.
	adminDir := filepath.Join(gitDir, "worktrees", "rate-limit")
	for _, name := range []string{"airlock-focus.md", "airlock-handoff.md", "airlock-review.md", "focus.md", "handoff.md", "review.md"} {
		if err := os.WriteFile(filepath.Join(adminDir, name), []byte("PLANTED-MARKER\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Only the wrapper that reads the mount is asked, through its test hooks.
	pi := runWrapper(t, "pi-wrapper.sh", wt, tasks, wrapperEnv(t, "AIRLOCK_PI_ARGS_ONLY=1"))
	if strings.Contains(pi, "PLANTED-MARKER") || strings.Contains(pi, adminDir) {
		t.Errorf("pi read the git directory:\n%s", pi)
	}
	claude := runWrapper(t, "claude-wrapper.sh", wt, tasks, wrapperEnv(t, "AIRLOCK_AGENT_ARGS_ONLY=1"))
	if strings.Contains(claude, "PLANTED-MARKER") {
		t.Errorf("claude read the git directory:\n%s", claude)
	}
	if !strings.Contains(claude, "FOCUS-MARKER") {
		t.Errorf("claude lost the task from the mount:\n%s", claude)
	}
}

// The project's own folder is no worktree, so it has no task, even when a
// worktree of the same name exists in the folder.
func TestWrappersGiveTheProjectFolderNoTask(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is needed to run the wrapper: " + err.Error())
	}
	wt, _, tasks := handoffWorktree(t)
	repo := filepath.Join(filepath.Dir(wt), "repo")
	out := runWrapper(t, "pi-wrapper.sh", repo, tasks, wrapperEnv(t, "AIRLOCK_PI_ARGS_ONLY=1"))
	if strings.Contains(out, "focus.md") || strings.Contains(out, "handoff.md") {
		t.Errorf("the project folder got a task:\n%s", out)
	}
}
