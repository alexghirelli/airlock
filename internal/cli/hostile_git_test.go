package cli

import (
	"path/filepath"
	"testing"

	"github.com/alexghirelli/airlock/internal/hostgit/hostgittest"
)

// The agent rewrites its worktree's .git file. The host's `remote push` (run
// from the worktree, the way a user would) and `wt ls` must not run what it
// points at. The positive control is plain git in the same setup.

func rewriteWorktreeGitFile(t *testing.T, wt string) string {
	t.Helper()
	marker := hostgittest.Marker(t)
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), marker)
	hostgittest.Write(t, filepath.Join(wt, ".git"), "gitdir: "+evil+"\n")
	if err := hostgittest.Naive(t, wt, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not run the attacker's fsmonitor program")
	}
	hostgittest.Reset(marker)
	return marker
}

func TestRemotePushFromAWorktreeDoesNotRunItsGitFile(t *testing.T) {
	_, wt := summaryProject(t)
	marker := rewriteWorktreeGitFile(t, wt)
	// Nothing reaches a server: the fake ssh fails before any push.
	t.Setenv("AIRLOCK_SSH", "/bin/false")
	t.Chdir(wt)
	code, _, errb := runCLI(t, "remote", "push", "box")
	if code == 0 {
		t.Error("remote push succeeded with a failing ssh")
	}
	t.Logf("remote push: exit %d: %s", code, errb)
	if hostgittest.Ran(marker) {
		t.Error("remote push ran the program the agent wrote into the worktree's .git file")
	}
}

func TestWtLsDoesNotRunTheWorktreesGitFile(t *testing.T) {
	proj, wt := summaryProject(t)
	marker := rewriteWorktreeGitFile(t, wt)
	t.Chdir(proj)
	code, out, errb := runCLI(t, "wt", "ls")
	if code != 0 {
		t.Fatalf("wt ls: exit %d: %s", code, errb)
	}
	if hostgittest.Ran(marker) {
		t.Errorf("wt ls ran the program the agent wrote into the worktree's .git file (output %q)", out)
	}
}
