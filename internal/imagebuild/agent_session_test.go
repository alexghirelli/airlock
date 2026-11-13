package imagebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alexghirelli/airlock"
)

// hasSession runs airlock-agent-session with the credentials folders of a test home.
func hasSession(t *testing.T, home, kind, dir string) bool {
	t.Helper()
	script, err := airlock.Assets.ReadFile("image/agent-session.sh")
	if err != nil {
		t.Fatal(err)
	}
	sp := filepath.Join(t.TempDir(), "agent-session.sh")
	if err := os.WriteFile(sp, script, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", sp, kind, dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"PI_CODING_AGENT_DIR=" + filepath.Join(home, "pi-agent"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude")}
	return cmd.Run() == nil
}

func TestAgentSessionHelper(t *testing.T) {
	home := t.TempDir()
	dir := "/work/my.app/.worktrees/rate-limit"
	for _, kind := range []string{"pi", "claude"} {
		if hasSession(t, home, kind, dir) {
			t.Errorf("%s: no session folder yet, but one was reported", kind)
		}
	}
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Pi: the path without its leading slash, slashes turned into dashes, between "--".
	write(filepath.Join(home, "pi-agent/sessions/--work-my.app-.worktrees-rate-limit--/2026-11-12T10-00-00Z_x.jsonl"))
	// Claude Code: every character that is not a letter or a digit turned into a dash.
	write(filepath.Join(home, "claude/projects/-work-my-app--worktrees-rate-limit/abc.jsonl"))
	for _, kind := range []string{"pi", "claude"} {
		if !hasSession(t, home, kind, dir) {
			t.Errorf("%s: a session exists for %s but none was reported", kind, dir)
		}
		if hasSession(t, home, kind, "/work/my.app") {
			t.Errorf("%s: the session of a worktree must not count for the project itself", kind)
		}
	}
	// A folder without a session file: Claude Code does not start with nothing to continue.
	if err := os.MkdirAll(filepath.Join(home, "claude/projects/-work-other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasSession(t, home, "claude", "/work/other") {
		t.Error("an empty session folder counted as a session")
	}
	if hasSession(t, home, "codex", dir) {
		t.Error("an unknown agent kind must report no session")
	}
}
