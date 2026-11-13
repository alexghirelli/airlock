package imagebuild

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock"
)

// herdrStub is a fake herdr that logs its calls. It answers the calls herdr-up
// makes before it would start an agent; anything else fails, which ends the run.
const herdrStub = `#!/bin/bash
echo "$*" >>"$HERDR_LOG"
case "$*" in
  "status server") echo "status: running" ;;
  "agent get "*) exit 1 ;;
  "agent list") [[ -z "${AGENTS_FAIL:-}" ]] || exit 1; cat "$AGENTS" ;;
  "workspace list") echo '{"result":{"workspaces":[]}}' ;;
  "workspace focus "*) echo '{"result":{}}' ;;
  *) exit 1 ;;
esac
`

// A workspace is started (or focused) for DIR: an agent already in DIR is the one
// to come back to, whatever its kind, and no second one is started next to it.
func TestHerdrUpReusesAnAgentAlreadyInTheFolder(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is needed by herdr-up.sh: " + err.Error())
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is needed to run herdr-up.sh: " + err.Error())
	}
	src, err := airlock.Assets.ReadFile("image/herdr-up.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	worktree := filepath.Join(dir, "rate-limit")
	elsewhere := filepath.Join(dir, "other")
	agents, err := json.Marshal(map[string]any{"result": map[string]any{"type": "agent_list", "agents": []map[string]any{
		{"agent": "pi", "agent_status": "idle", "cwd": elsewhere, "name": "pi", "pane_id": "w8:p1", "workspace_id": "w8"},
		{"agent": "claude", "agent_status": "idle", "cwd": worktree, "name": "claude", "pane_id": "w7:p1", "workspace_id": "w7"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for name, text := range map[string]string{
		"herdr":                 herdrStub,
		"airlock-agent-session": "#!/bin/bash\nexit 1\n", // no conversation to continue
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(bin, "herdr-up.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(agentsFile, agents, 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(agentsFail bool, args ...string) string {
		t.Helper()
		logFile := filepath.Join(t.TempDir(), "herdr.log")
		env := []string{
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"AIRLOCK_PROJECT_DIR=" + filepath.Join(dir, "project"),
			"HERDR_LOG=" + logFile,
			"AGENTS=" + agentsFile,
		}
		if agentsFail {
			env = append(env, "AGENTS_FAIL=1")
		}
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = env
		cmd.Dir = dir
		_ = cmd.Run() // the stub fails the start on purpose; the calls are what matters
		b, rerr := os.ReadFile(logFile)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return string(b)
	}

	calls := run(false, "--agent", "pi", "--workspace", worktree)
	if !strings.Contains(calls, "workspace focus w7") {
		t.Errorf("the claude workspace was not focused:\n%s", calls)
	}
	if strings.Contains(calls, "workspace create") {
		t.Errorf("a second workspace was created next to the agent in the folder:\n%s", calls)
	}

	calls = run(false, "--agent", "pi", "--workspace", worktree, "--no-focus")
	if strings.Contains(calls, "workspace focus") || strings.Contains(calls, "workspace create") {
		t.Errorf("--no-focus still moved the view or started a workspace:\n%s", calls)
	}

	// An agent in a subfolder or another folder is not the one of this worktree.
	calls = run(false, "--agent", "pi", "--workspace", filepath.Join(dir, "third"))
	if !strings.Contains(calls, "workspace create") {
		t.Errorf("a folder with no agent did not get a workspace:\n%s", calls)
	}

	// A list that cannot be read falls back to the usual start.
	calls = run(true, "--agent", "pi", "--workspace", worktree)
	if !strings.Contains(calls, "workspace create") {
		t.Errorf("an unreadable agent list did not fall back to the start:\n%s", calls)
	}
}
