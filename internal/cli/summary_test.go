package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/summary"
)

// summaryHome points HOME, the XDG directories and git's global configuration at
// a temporary place, so the test writes nothing of the user's.
func summaryHome(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// gitIn runs git in dir without the caller's repository variables.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_INDEX_FILE" {
			continue
		}
		env = append(env, kv)
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// summaryProject is a repository with one commit on main and a worktree "feat"
// with one commit of its own, and an uncommitted change.
func summaryProject(t *testing.T) (proj, wt string) {
	t.Helper()
	summaryHome(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj = filepath.Join(base, "proj")
	gitIn(t, base, "init", "-q", "-b", "main", proj)
	if err := os.WriteFile(filepath.Join(proj, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, proj, "add", "a.txt")
	gitIn(t, proj, "commit", "-qm", "add a")
	wt = filepath.Join(proj, ".worktrees", "feat")
	gitIn(t, proj, "worktree", "add", "-q", "--no-track", "-b", "feat", wt)
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return proj, wt
}

// runCLI runs the command line and returns its exit code and standard output.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

func baselinePath(t *testing.T, proj string) string {
	t.Helper()
	p, err := sandbox.Resolve(proj)
	if err != nil {
		t.Fatal(err)
	}
	return auditPath(p)
}

func TestSummaryJSONWritesNoBaseline(t *testing.T) {
	proj, _ := summaryProject(t)
	t.Chdir(proj)
	code, out, errb := runCLI(t, "summary", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	var s summary.Summary
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if s.Name != filepath.Base(proj) || s.Branch != "main" || s.Audit == nil || s.Audit.Baseline || len(s.Audit.Changes) != 0 {
		t.Errorf("project summary: %+v audit %+v", s, s.Audit)
	}
	if len(s.Files) != 0 || s.Uncommitted != 0 || s.BaseRef != "" {
		t.Errorf("main has no base and no changes: %+v", s)
	}
	if _, err := os.Stat(baselinePath(t, proj)); !os.IsNotExist(err) {
		t.Errorf("summary wrote the audit baseline (stat: %v)", err)
	}
}

func TestSummaryReportsAuditAgainstBaseline(t *testing.T) {
	proj, _ := summaryProject(t)
	t.Chdir(proj)
	if code, _, errb := runCLI(t, "audit", "--accept"); code != 0 {
		t.Fatalf("audit --accept: exit %d: %s", code, errb)
	}
	before, err := os.ReadFile(baselinePath(t, proj))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".envrc"), []byte("export X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // reading twice reports the same change: nothing is absorbed
		code, out, errb := runCLI(t, "summary", "--json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb)
		}
		var s summary.Summary
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatal(err)
		}
		if s.Audit == nil || !s.Audit.Baseline || len(s.Audit.Changes) != 1 || !strings.Contains(s.Audit.Changes[0], ".envrc") {
			t.Fatalf("run %d: audit %+v", i, s.Audit)
		}
	}
	after, err := os.ReadFile(baselinePath(t, proj))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("summary changed the audit baseline")
	}
	code, out, _ := runCLI(t, "summary")
	if code != 0 || !strings.Contains(out, "audit: 1 change to host-executable files, project-wide (airlock audit)") {
		t.Errorf("text audit line missing (exit %d):\n%s", code, out)
	}
}

func TestSummaryWorktreeByNameAndFromInside(t *testing.T) {
	proj, wt := summaryProject(t)
	t.Chdir(proj)
	code, out, errb := runCLI(t, "summary", "feat", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	var s summary.Summary
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "feat" || s.Branch != "feat" || len(s.Files) != 1 || s.Files[0].Path != "a.txt" || s.Files[0].Added != 1 {
		t.Errorf("worktree by name: %+v", s)
	}
	// Inside the worktree, with no name, the worktree is what is summarised.
	t.Chdir(wt)
	code, out, _ = runCLI(t, "summary", "--json")
	if code != 0 || !strings.Contains(out, `"name": "feat"`) {
		t.Errorf("no name inside a worktree (exit %d): %s", code, out)
	}
}

func TestSummaryErrors(t *testing.T) {
	proj, _ := summaryProject(t)
	t.Chdir(proj)
	if code, _, errb := runCLI(t, "summary", "nope"); code != 1 || !strings.Contains(errb, `no worktree "nope"`) {
		t.Errorf("unknown worktree: exit %d: %s", code, errb)
	}
	if code, _, _ := runCLI(t, "summary", "a", "b"); code != 2 {
		t.Errorf("two names: exit %d, want 2", code)
	}
}
