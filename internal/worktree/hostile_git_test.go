package worktree

import (
	"path/filepath"
	"testing"

	"github.com/alexghirelli/airlock/internal/hostgit/hostgittest"
)

// The positive control is plain git in the same setup; then List must not run
// the agent's program.

func TestListDoesNotRunTheWorktreesRewrittenGitFile(t *testing.T) {
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
	repo := &Repo{Root: main, Dir: ".worktrees"}
	list, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "feat" || !list[0].Dirty {
		t.Errorf("List = %+v, want the dirty worktree feat", list)
	}
	if hostgittest.Ran(marker) {
		t.Error("List ran the program the agent wrote into the worktree's .git file")
	}
}

func TestListRefusesTheMainCheckoutsCommondir(t *testing.T) {
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
	repo := &Repo{Root: main, Dir: ".worktrees"}
	if _, err := repo.List(); err == nil {
		t.Error("List worked on a checkout whose .git has a commondir file")
	}
	if hostgittest.Ran(marker) {
		t.Error("the commondir target ran")
	}
}
