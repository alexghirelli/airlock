package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

// A nested repository (a gitlink in the worktree) keeps its configuration inside
// the working tree, which the agent writes. Asking whether it is dirty makes git
// start a child process there, and a clean filter defined in that configuration
// would run on the host. Submodules are never looked into.
func TestListDoesNotRunAFilterOfANestedRepository(t *testing.T) {
	main, wt := hostgittest.Project(t)
	marker := hostgittest.Marker(t)
	clean := filepath.Join(hostgittest.Dir(t), "clean.sh")
	hostgittest.Write(t, clean, "#!/bin/sh\ntouch '"+marker+"'\ncat\n")
	if err := os.Chmod(clean, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(wt, "sub")
	hostgittest.Git(t, wt, "init", "-q", "-b", "main", sub)
	hostgittest.Write(t, filepath.Join(sub, "f"), "AAAA\n")
	hostgittest.Write(t, filepath.Join(sub, ".gitattributes"), "* filter=m\n")
	hostgittest.Git(t, sub, "add", "f", ".gitattributes")
	hostgittest.Git(t, sub, "commit", "-qm", "sub")
	hostgittest.Git(t, wt, "add", "sub")
	hostgittest.Git(t, wt, "commit", "-qm", "gitlink")
	// The filter exists only now, after the commit, and the file looks modified to
	// git's stat check (same size, other time), so git must read it through the filter.
	hostgittest.Git(t, sub, "config", "filter.m.clean", clean)
	hostgittest.Write(t, filepath.Join(sub, "f"), "BBBB\n")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(sub, "f"), past, past); err != nil {
		t.Fatal(err)
	}

	if err := hostgittest.Naive(t, wt, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not run the nested repository's filter")
	}
	hostgittest.Reset(marker)
	if err := os.Chtimes(filepath.Join(sub, "f"), past, past); err != nil {
		t.Fatal(err)
	}

	repo := &Repo{Root: main, Dir: ".worktrees"}
	if _, err := repo.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	if hostgittest.Ran(marker) {
		t.Error("List ran a filter defined in a nested repository of the worktree")
	}
}
