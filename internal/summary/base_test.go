package summary

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectPushedFeatureBranchIgnoresItsOwnCopy(t *testing.T) {
	p := newProject(t)
	testGit(t, p.wt, "push", "-q", "-u", "origin", "feat")
	s, err := Collect(p.wt, "feat", "")
	if err != nil {
		t.Fatal(err)
	}
	// Its upstream is its own tip on origin: the base is the default branch.
	if s.BaseRef != "origin/main" || s.CommitCount != 3 {
		t.Errorf("pushed feature branch: base %q, %d commits", s.BaseRef, s.CommitCount)
	}
}

// soloRepo is a repository with main (one commit) and no remote.
func soloRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	dir := filepath.Join(tempDir(t), "solo")
	testGit(t, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	commitFile(t, dir, "a.txt", "one\n")
	return dir
}

func TestCollectDetachedHead(t *testing.T) {
	dir := soloRepo(t)
	testGit(t, dir, "checkout", "-q", "--detach")
	commitFile(t, dir, "b.txt", "two\n")
	s, err := Collect(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Branch != "" || s.BaseRef != "main" || s.CommitCount != 1 {
		t.Errorf("detached: branch %q, base %q, %d commits", s.Branch, s.BaseRef, s.CommitCount)
	}
	if !strings.HasPrefix(s.Text(), "solo  detached at ") {
		t.Errorf("detached header:\n%s", s.Text())
	}
}

func TestCollectUnrelatedHistories(t *testing.T) {
	dir := soloRepo(t)
	testGit(t, dir, "checkout", "-q", "--orphan", "other")
	commitFile(t, dir, "x.txt", "orphan\n")
	s, err := Collect(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseRef != "" || s.Base != "" || len(s.Notes) != 1 || !strings.Contains(s.Notes[0], "no merge-base between HEAD and main") {
		t.Errorf("unrelated histories: base %q, notes %q", s.BaseRef, s.Notes)
	}
}

func TestCollectLeavesTheCheckoutAlone(t *testing.T) {
	dir := soloRepo(t)
	before := testGit(t, dir, "status", "--porcelain", "--ignored")
	if _, err := Collect(dir, "", ""); err != nil {
		t.Fatal(err)
	}
	if after := testGit(t, dir, "status", "--porcelain", "--ignored"); after != before {
		t.Errorf("Collect changed the checkout:\n%s\n%s", before, after)
	}
}

func TestCollectIgnoresGitConfigFromTheEnvironment(t *testing.T) {
	p := newProject(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
	t.Setenv("GIT_CONFIG_VALUE_0", "40")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.abbrev=39'")
	s, err := Collect(p.wt, "feat", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Head) >= 12 {
		t.Errorf("the environment's git config reached the summary: head %q", s.Head)
	}
}
