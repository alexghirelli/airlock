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

// handoffWorktree is a git repository whose git directory holds the task files
// the wrappers read (as `airlock wt focus` and `airlock wt handoff` write them).
func handoffWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	init := exec.Command("git", "init", "-q", dir)
	init.Env = wrapperEnv(t)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	gitDir := gitDirOf(t, dir)
	for name, text := range map[string]string{
		"airlock-focus.md":   "FOCUS-MARKER\n",
		"airlock-handoff.md": "HANDOFF-MARKER\n",
	} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runWrapper runs an image script from the embedded assets, in dir, with args.
func runWrapper(t *testing.T, script, dir string, env []string, args ...string) string {
	t.Helper()
	src, err := airlock.Assets.ReadFile("image/" + script)
	if err != nil {
		t.Fatal(err)
	}
	sp := filepath.Join(t.TempDir(), script)
	if err := os.WriteFile(sp, src, 0o755); err != nil {
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
	dir := handoffWorktree(t)
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
		out := runWrapper(t, "pi-wrapper.sh", dir, env, c.args...)
		if got := strings.Contains(out, "airlock-handoff.md"); got != c.wantHandoff {
			t.Errorf("pi %v: handoff included = %v, want %v:\n%s", c.args, got, c.wantHandoff, out)
		}
		if !strings.Contains(out, "airlock-focus.md") {
			t.Errorf("pi %v: the focus is no longer passed:\n%s", c.args, out)
		}
	}
	// Without a briefing file there is nothing to add, fresh or not.
	if err := os.Remove(filepath.Join(gitDirOf(t, dir), "airlock-handoff.md")); err != nil {
		t.Fatal(err)
	}
	if out := runWrapper(t, "pi-wrapper.sh", dir, env); strings.Contains(out, "airlock-handoff.md") {
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
	dir := handoffWorktree(t)
	env := wrapperEnv(t, "AIRLOCK_AGENT_ARGS_ONLY=1")
	fresh := runWrapper(t, "claude-wrapper.sh", dir, env)
	if !strings.Contains(fresh, "HANDOFF-MARKER") || !strings.Contains(fresh, "FOCUS-MARKER") {
		t.Errorf("fresh claude does not get the briefing and the focus:\n%s", fresh)
	}
	resumed := runWrapper(t, "claude-wrapper.sh", dir, env, "--continue")
	if !strings.Contains(resumed, "HANDOFF-MARKER") {
		t.Errorf("a resumed claude lost the briefing:\n%s", resumed)
	}
	if !strings.Contains(resumed, "FOCUS-MARKER") {
		t.Errorf("a resumed claude lost the focus:\n%s", resumed)
	}
}

func gitDirOf(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir")
	cmd.Env = wrapperEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
