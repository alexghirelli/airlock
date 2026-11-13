package summary

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/hostgit/hostgittest"
)

// Each test runs the attack with plain git first (the positive control), then
// Collect, which must not run the agent's program.

// The agent rewrites the worktree's .git file to point at a repository it owns.
func TestCollectDoesNotRunTheWorktreesRewrittenGitFile(t *testing.T) {
	main, wt := hostgittest.Project(t)
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
	if _, err := Collect(main, wt, "feat", ""); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if hostgittest.Ran(marker) {
		t.Error("Collect ran the program the agent wrote into the worktree's .git file")
	}
}

// The agent writes .git/commondir in the main checkout, pointing at its own repository.
func TestCollectRefusesTheMainCheckoutsCommondir(t *testing.T) {
	main, _ := hostgittest.Project(t)
	marker := hostgittest.Marker(t)
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), marker)
	hostgittest.Write(t, filepath.Join(main, ".git", "commondir"), evil+"\n")
	if err := hostgittest.Naive(t, main, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not follow commondir")
	}
	hostgittest.Reset(marker)
	if _, err := Collect(main, main, "", ""); err == nil {
		t.Error("Collect read a checkout whose .git has a commondir file")
	}
	if hostgittest.Ran(marker) {
		t.Error("Collect ran the commondir target")
	}
}

// The agent puts a filter in the worktree's admin config.worktree and
// `* filter=x` in the shared info/attributes, so a modified file goes through
// the filter when git hashes it.
func TestCollectIgnoresFilterDrivenByAgentConfigWorktree(t *testing.T) {
	main, wt := hostgittest.Project(t)
	hostgittest.Git(t, main, "config", "extensions.worktreeConfig", "true")
	marker := hostgittest.Marker(t)
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), marker)
	clean := filepath.Join(filepath.Dir(filepath.Dir(evil)), "clean.sh")
	admin := filepath.Join(main, ".git", "worktrees", "feat")
	hostgittest.Write(t, filepath.Join(main, ".git", "info", "attributes"), "* filter=x\n")
	hostgittest.Write(t, filepath.Join(admin, "config.worktree"), "[filter \"x\"]\n\tclean = "+clean+"\n")
	if err := hostgittest.Naive(t, wt, "diff", "--numstat"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not run the filter from config.worktree")
	}
	hostgittest.Reset(marker)
	if _, err := Collect(main, wt, "feat", ""); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if hostgittest.Ran(marker) {
		t.Error("Collect ran a filter from the agent's config.worktree")
	}
}

// The same from the main checkout's own config.worktree, which the agent can write.
func TestCollectIgnoresFilterInTheMainConfigWorktree(t *testing.T) {
	main, _ := hostgittest.Project(t)
	hostgittest.Git(t, main, "config", "extensions.worktreeConfig", "true")
	marker := hostgittest.Marker(t)
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), marker)
	clean := filepath.Join(filepath.Dir(filepath.Dir(evil)), "clean.sh")
	hostgittest.Write(t, filepath.Join(main, ".git", "info", "attributes"), "* filter=x\n")
	hostgittest.Write(t, filepath.Join(main, ".git", "config.worktree"), "[filter \"x\"]\n\tclean = "+clean+"\n")
	if err := hostgittest.Naive(t, main, "diff", "--numstat"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not run the filter from the main config.worktree")
	}
	hostgittest.Reset(marker)
	if _, err := Collect(main, main, "", ""); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if hostgittest.Ran(marker) {
		t.Error("Collect ran a filter from the main checkout's config.worktree")
	}
}

// A listing past the output limit is reported, not a failure.
func TestCollectReportsTooManyChanges(t *testing.T) {
	main, wt := hostgittest.Project(t)
	for i := 0; i < 5; i++ {
		hostgittest.Write(t, filepath.Join(wt, "many", "file"+string(rune('a'+i))+".txt"), "x\n")
	}
	old := outputLimit
	outputLimit = 16
	t.Cleanup(func() { outputLimit = old })
	s, err := Collect(main, wt, "feat", "")
	if err != nil {
		t.Fatalf("Collect failed past the limit: %v", err)
	}
	if !strings.Contains(strings.Join(s.Notes, "\n"), "too many changes to list") {
		t.Errorf("no note past the limit: %+v", s.Notes)
	}
	if len(s.Untracked) != 0 {
		t.Errorf("the untracked list past the limit was kept: %+v", s.Untracked)
	}
}
