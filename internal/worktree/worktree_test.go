package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseChange(t *testing.T) {
	g := func(n, ps int) Change { return Change{Forge: Gerrit, Number: n, Patchset: ps} }
	good := map[string]Change{
		"12345":     {Number: 12345},
		"12345/3":   g(12345, 3),
		"12345,3":   g(12345, 3),
		" 7 ":       {Number: 7},
		"gerrit:77": {Forge: Gerrit, Number: 77},
		"pr:12":     {Forge: GitHub, Number: 12},
		"mr:45":     {Forge: GitLab, Number: 45},
		"bb:8":      {Forge: Bitbucket, Number: 8},

		"refs/changes/45/12345/9":     g(12345, 9),
		"refs/pull/12/head":           {Forge: GitHub, Number: 12},
		"refs/pull-requests/8/from":   {Forge: Bitbucket, Number: 8},
		"refs/merge-requests/45/head": {Forge: GitLab, Number: 45},

		"https://gerrit.example.org/r/c/mediawiki/core/+/12345":       {Forge: Gerrit, Number: 12345, RepoHost: "gerrit.example.org", RepoPath: "mediawiki/core"},
		"https://gerrit.example.org/r/c/mediawiki/core/+/12345/7":     {Forge: Gerrit, Number: 12345, Patchset: 7, RepoHost: "gerrit.example.org", RepoPath: "mediawiki/core"},
		"https://gerrit.example.org/r/c/mediawiki/core/+/12345/7?x=y": {Forge: Gerrit, Number: 12345, Patchset: 7, RepoHost: "gerrit.example.org", RepoPath: "mediawiki/core"},
		"https://gerrit.example.org/#/c/12345/2":                      {Forge: Gerrit, Number: 12345, Patchset: 2, RepoHost: "gerrit.example.org"},
		"https://gerrit.example.org/#/c/mediawiki/core/+/12345/2":     {Forge: Gerrit, Number: 12345, Patchset: 2, RepoHost: "gerrit.example.org", RepoPath: "mediawiki/core"},

		"https://github.com/acme/widgets/pull/321":                                      {Forge: GitHub, Number: 321, RepoHost: "github.com", RepoPath: "acme/widgets"},
		"https://github.com/acme/widgets/pull/321/files#diff-abc":                       {Forge: GitHub, Number: 321, RepoHost: "github.com", RepoPath: "acme/widgets"},
		"https://git.example.org/team/sub/proj/-/merge_requests/9":                      {Forge: GitLab, Number: 9, RepoHost: "git.example.org", RepoPath: "team/sub/proj"},
		"https://gitlab.com/acme/widgets/-/merge_requests/9/diffs":                      {Forge: GitLab, Number: 9, RepoHost: "gitlab.com", RepoPath: "acme/widgets"},
		"https://bb.example.org/projects/PROJ/repos/widgets/pull-requests/8/overview":   {Forge: Bitbucket, Number: 8, RepoHost: "bb.example.org", RepoPath: "proj/widgets"},
		"https://git.example.org/bitbucket/projects/KEY/repos/api/pull-requests/3/diff": {Forge: Bitbucket, Number: 3, RepoHost: "git.example.org", RepoPath: "key/api"},
		"https://gitlab.example.org/acme/widgets/merge_requests/14":                     {Forge: GitLab, Number: 14, RepoHost: "gitlab.example.org", RepoPath: "acme/widgets"},
	}
	for in, want := range good {
		got, err := ParseChange(in)
		if err != nil || got != want {
			t.Errorf("ParseChange(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"https://bitbucket.org/acme/widgets/pull-requests/8", "bb:8/2", "", "abc", "0", "https://example.org/", "https://github.com/acme/widgets", "12345/x", "-3", "pr:12/3", "mr:4,2", "ftp:9"} {
		if c, err := ParseChange(in); err == nil {
			t.Errorf("ParseChange(%q) = %+v, want an error", in, c)
		}
	}
}

func TestChangeRefsAndNames(t *testing.T) {
	cases := []struct {
		c                          Change
		ref, fetched, name, branch string
	}{
		{Change{Forge: Gerrit, Number: 12345, Patchset: 3}, "refs/changes/45/12345/3", "refs/airlock/changes/12345/3", "review-12345", "review/12345"},
		{Change{Forge: Gerrit, Number: 7, Patchset: 1}, "refs/changes/07/7/1", "refs/airlock/changes/7/1", "review-7", "review/7"},
		{Change{Forge: GitHub, Number: 12}, "refs/pull/12/head", "refs/airlock/pull/12", "review-pr-12", "review/pr-12"},
		{Change{Forge: GitLab, Number: 45}, "refs/merge-requests/45/head", "refs/airlock/merge-requests/45", "review-mr-45", "review/mr-45"},
		{Change{Forge: Bitbucket, Number: 8}, "refs/pull-requests/8/from", "refs/airlock/pull-requests/8", "review-bb-8", "review/bb-8"},
	}
	for _, c := range cases {
		if c.c.serverRef() != c.ref || c.c.fetched() != c.fetched || c.c.Name() != c.name || c.c.Branch() != c.branch {
			t.Errorf("%+v: ref %s fetched %s name %s branch %s", c.c, c.c.serverRef(), c.c.fetched(), c.c.Name(), c.c.Branch())
		}
	}
}

func TestSplitRemoteURL(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/acme/widgets.git":                 {"github.com", "acme/widgets"},
		"git@github.com:acme/widgets.git":                     {"github.com", "acme/widgets"},
		"ssh://git@gitlab.example.org:2222/team/sub/proj.git": {"gitlab.example.org", "team/sub/proj"},
		"ssh://me@gerrit.example.org:29418/mediawiki/core":    {"gerrit.example.org", "mediawiki/core"},
		"https://www.GitHub.com/acme/widgets/":                {"github.com", "acme/widgets"},
		"https://gerrit.example.org/r/mediawiki/core":         {"gerrit.example.org", "r/mediawiki/core"},
	}
	for in, want := range cases {
		if h, p := splitRemoteURL(in); h != want[0] || p != want[1] {
			t.Errorf("splitRemoteURL(%q) = %q, %q; want %q, %q", in, h, p, want[0], want[1])
		}
	}
	if !pathMatches("PROJ/widgets", "proj/widgets") || !pathMatches("scm/proj/widgets", "proj/widgets") {
		t.Error("project keys must match whatever their case")
	}
	if !pathMatches("r/mediawiki/core", "mediawiki/core") || pathMatches("r/mediawiki/core-extra", "mediawiki/core") || pathMatches("x", "") {
		t.Error("pathMatches")
	}
}

func TestPathRejectsBadNames(t *testing.T) {
	r := &Repo{Root: "/r", Dir: ".worktrees"}
	for _, n := range []string{"", "../x", "a/b", ".hidden", "a..b", "-x", "a b"} {
		if _, err := r.Path(n); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if _, err := (&Repo{Root: "/r"}).Path("ok"); err == nil {
		t.Error("worktrees disabled but a path was returned")
	}
	if p, err := r.Path("feat-1.2"); err != nil || p != "/r/.worktrees/feat-1.2" {
		t.Errorf("path = %q, %v", p, err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-qm", "edit "+file)
	return git(t, dir, "rev-parse", "HEAD")
}

// fixture is a fake Gerrit (a bare repo with refs/changes) and a clone of it.
type fixture struct {
	repo   *Repo
	server string
	main   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(tmp, "server.git")
	git(t, tmp, "init", "-q", "--bare", "-b", "main", server)
	main := filepath.Join(tmp, "core")
	git(t, tmp, "init", "-q", "-b", "main", main)
	commit(t, main, "a.txt", "one\n")
	git(t, main, "remote", "add", "origin", server)
	git(t, main, "push", "-q", "origin", "main")
	return &fixture{repo: &Repo{Root: main, Dir: ".worktrees"}, server: server, main: main}
}

// patchset publishes a commit on top of main as refs/changes/NN/N/P.
func (f *fixture) patchset(t *testing.T, change, ps int, content string) string {
	t.Helper()
	tmp := t.TempDir()
	clone := filepath.Join(tmp, "c")
	git(t, tmp, "clone", "-q", f.server, clone)
	sha := commit(t, clone, "change.txt", content)
	git(t, clone, "push", "-q", "origin", "HEAD:"+Change{Forge: Gerrit, Number: change, Patchset: ps}.serverRef())
	return sha
}

// publish puts a commit on top of main on the server under any ref, as a forge
// does for pull and merge requests.
func (f *fixture) publish(t *testing.T, ref, content string) string {
	t.Helper()
	tmp := t.TempDir()
	clone := filepath.Join(tmp, "c")
	git(t, tmp, "clone", "-q", f.server, clone)
	sha := commit(t, clone, "change.txt", content)
	git(t, clone, "push", "-q", "-f", "origin", "HEAD:"+ref)
	return sha
}

func TestAddCreatesBranchFromHeadAndLocksIt(t *testing.T) {
	f := newFixture(t)
	path, err := f.repo.Add("feat", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, path, "branch", "--show-current"); got != "feat" {
		t.Errorf("branch = %q", got)
	}
	if want := filepath.Join(f.main, ".worktrees", "feat"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if out := git(t, f.main, "worktree", "list", "--porcelain"); !strings.Contains(out, "locked") {
		t.Errorf("worktree not locked (git worktree prune could drop it):\n%s", out)
	}
	if _, err := f.repo.Add("feat", "", "", false); err == nil {
		t.Error("adding the same worktree twice succeeded")
	}
}

func TestAddUsesExistingAndRemoteBranches(t *testing.T) {
	f := newFixture(t)
	git(t, f.main, "branch", "old")
	path, err := f.repo.Add("w-old", "old", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, path, "branch", "--show-current"); got != "old" {
		t.Errorf("branch = %q", got)
	}
	if _, err := f.repo.Add("w-again", "old", "", false); err == nil {
		t.Error("a branch checked out elsewhere was checked out twice")
	}
	if _, err := f.repo.Add("w-from", "old", "main", false); err == nil {
		t.Error("--from on an existing branch was accepted")
	}

	// A branch only the remote has is tracked.
	git(t, f.main, "push", "-q", "origin", "main:refs/heads/remote-only")
	git(t, f.main, "fetch", "-q", "origin")
	path, err = f.repo.Add("w-remote", "remote-only", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if up := git(t, path, "rev-parse", "--abbrev-ref", "remote-only@{upstream}"); up != "origin/remote-only" {
		t.Errorf("upstream = %q", up)
	}
}

func TestReviewFetchesLatestPatchsetOnALocalBranch(t *testing.T) {
	f := newFixture(t)
	f.patchset(t, 12345, 1, "v1\n")
	sha2 := f.patchset(t, 12345, 2, "v2\n")

	res, err := f.repo.Review(Change{Number: 12345}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Change.Patchset != 2 || !res.Created || res.Branch != "review/12345" {
		t.Errorf("result = %+v", res)
	}
	if got := git(t, res.Path, "rev-parse", "HEAD"); got != sha2 {
		t.Errorf("HEAD = %s, want patchset 2 (%s)", got, sha2)
	}
	if got := git(t, res.Path, "branch", "--show-current"); got != "review/12345" {
		t.Errorf("branch = %q, want a local branch, not a detached HEAD", got)
	}
	if filepath.Base(res.Path) != "review-12345" {
		t.Errorf("path = %s", res.Path)
	}
}

func TestReviewMovesToANewPatchsetOnlyWhenNothingIsLost(t *testing.T) {
	f := newFixture(t)
	f.patchset(t, 55, 1, "v1\n")
	res, err := f.repo.Review(Change{Number: 55}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	// A clean review follows the new patchset.
	sha2 := f.patchset(t, 55, 2, "v2\n")
	res, err = f.repo.Review(Change{Number: 55}, "", "", false)
	if err != nil || res.Note != "" || res.Created {
		t.Fatalf("update: %+v, %v", res, err)
	}
	if got := git(t, res.Path, "rev-parse", "HEAD"); got != sha2 {
		t.Errorf("HEAD = %s, want %s", got, sha2)
	}

	// Uncommitted changes are kept.
	if err := os.WriteFile(filepath.Join(res.Path, "change.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.patchset(t, 55, 3, "v3\n")
	res, err = f.repo.Review(Change{Number: 55}, "", "", false)
	if err != nil || !strings.Contains(res.Note, "uncommitted") {
		t.Fatalf("expected a note about uncommitted changes: %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(res.Path, "change.txt")); string(b) != "mine\n" {
		t.Errorf("uncommitted work lost: %q", b)
	}

	// So are local commits.
	git(t, res.Path, "checkout", "-q", "--", "change.txt")
	mine := commit(t, res.Path, "change.txt", "mine committed\n")
	f.patchset(t, 55, 4, "v4\n")
	res, err = f.repo.Review(Change{Number: 55}, "", "", false)
	if err != nil || !strings.Contains(res.Note, "local commits") {
		t.Fatalf("expected a note about local commits: %+v, %v", res, err)
	}
	if got := git(t, res.Path, "rev-parse", "HEAD"); got != mine {
		t.Errorf("local commit lost: HEAD = %s, want %s", got, mine)
	}
}

func TestReviewErrors(t *testing.T) {
	f := newFixture(t)
	if _, err := f.repo.Review(Change{Number: 999}, "", "", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing change: %v", err)
	}
	f.patchset(t, 7, 1, "x\n")
	if _, err := f.repo.Review(Change{Number: 7, Patchset: 9}, "", "", false); err == nil {
		t.Error("a patchset that does not exist was accepted")
	}
	f.repo.GerritRemote = "nowhere"
	if _, err := f.repo.Review(Change{Number: 7}, "", "", false); err == nil {
		t.Error("an unknown remote was accepted")
	}
}

func TestListAndRemove(t *testing.T) {
	f := newFixture(t)
	path, err := f.repo.Add("feat", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	list, err := f.repo.List()
	if err != nil || len(list) != 1 || list[0].Name != "feat" || list[0].Branch != "feat" || list[0].Dirty {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if list, _ = f.repo.List(); !list[0].Dirty {
		t.Error("untracked file not reported as dirty")
	}
	if _, err := f.repo.Remove("feat", false); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("remove with changes: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("worktree removed despite uncommitted changes")
	}
	branch, err := f.repo.Remove("feat", true)
	if err != nil || branch != "feat" {
		t.Fatalf("forced remove: %q, %v", branch, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("worktree directory still there")
	}
	if git(t, f.main, "branch", "--list", "feat") == "" {
		t.Error("the branch was deleted; it must be kept")
	}
	if _, err := f.repo.Remove("feat", false); err == nil {
		t.Error("removing a missing worktree succeeded")
	}
}

func TestStaleWorktreesArePruned(t *testing.T) {
	f := newFixture(t)
	gone, err := f.repo.Add("gone", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Add("kept", "", "", false); err != nil {
		t.Fatal(err)
	}
	if stale, err := f.repo.Stale(); err != nil || len(stale) != 0 {
		t.Fatalf("nothing is stale yet: %+v, %v", stale, err)
	}
	if err := os.RemoveAll(gone); err != nil { // deleted by hand
		t.Fatal(err)
	}
	// airlock locks its worktrees, so plain git prune would leave this one behind.
	git(t, f.main, "worktree", "prune")
	stale, err := f.repo.Stale()
	if err != nil || len(stale) != 1 || stale[0].Name != "gone" {
		t.Fatalf("stale = %+v, %v; want only gone", stale, err)
	}
	if err := f.repo.PruneStale(stale); err != nil {
		t.Fatal(err)
	}
	list, _ := f.repo.List()
	if len(list) != 1 || list[0].Name != "kept" {
		t.Errorf("after pruning: %+v", list)
	}
	if git(t, f.main, "branch", "--list", "gone") == "" {
		t.Error("the branch was deleted; it must be kept")
	}
}

func TestIsolatedWorktreesLiveInTheirOwnFolder(t *testing.T) {
	f := newFixture(t)
	shared, err := f.repo.Add("mine", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := f.repo.Add("theirs", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.main, ".worktrees", "mine"); shared != want {
		t.Errorf("shared path = %q, want %q", shared, want)
	}
	if want := filepath.Join(f.main, ".worktrees", ".isolated", "theirs"); isolated != want {
		t.Errorf("isolated path = %q, want %q", isolated, want)
	}
	// A name is unique across both places, and found in either.
	for _, iso := range []bool{false, true} {
		if _, err := f.repo.Add("mine", "other", "", iso); err == nil {
			t.Errorf("name reused (isolated=%v)", iso)
		}
	}
	if p, iso, ok := f.repo.Find("theirs"); !ok || !iso || p != isolated {
		t.Errorf("Find(theirs) = %q, %v, %v", p, iso, ok)
	}
	list, err := f.repo.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, e := range list {
		if want := e.Name == "theirs"; e.Isolated != want {
			t.Errorf("%s: isolated = %v", e.Name, e.Isolated)
		}
	}
	if _, err := f.repo.Remove("theirs", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(isolated); err == nil {
		t.Error("isolated worktree not removed")
	}
}

func TestReviewKeepsAnExistingWorktreeWhereItIs(t *testing.T) {
	f := newFixture(t)
	f.patchset(t, 31, 1, "v1\n")
	res, err := f.repo.Review(Change{Number: 31}, "", "", true)
	if err != nil || !res.Isolated || !strings.Contains(res.Path, ".isolated") {
		t.Fatalf("first review: %+v, %v", res, err)
	}
	f.patchset(t, 31, 2, "v2\n")
	res, err = f.repo.Review(Change{Number: 31}, "", "", false)
	if err != nil || !res.Isolated || res.Created {
		t.Errorf("an update must not move the worktree to the other place: %+v, %v", res, err)
	}
}

func TestReviewWritesTheTaskOutsideTheCheckout(t *testing.T) {
	f := newFixture(t)
	f.patchset(t, 88, 1, "v1\n")
	res, err := f.repo.Review(Change{Number: 88}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	brief, err := os.ReadFile(filepath.Join(AdminDir(res.Path), briefFile))
	if err != nil {
		t.Fatalf("no review brief: %v", err)
	}
	for _, want := range []string{"change 88, patchset 1", "by t", "review/88", "Do not push"} {
		if !strings.Contains(string(brief), want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	if st := git(t, res.Path, "status", "--porcelain"); st != "" {
		t.Errorf("the task files show up in git status:\n%s", st)
	}

	// A newer patchset rewrites the brief; the user's focus is untouched.
	if err := SetFocus(f.main, res.Path, "SQL injection only"); err != nil {
		t.Fatal(err)
	}
	f.patchset(t, 88, 2, "v2\n")
	if _, err := f.repo.Review(Change{Number: 88}, "", "", false); err != nil {
		t.Fatal(err)
	}
	brief, _ = os.ReadFile(filepath.Join(AdminDir(res.Path), briefFile))
	if !strings.Contains(string(brief), "patchset 2") {
		t.Errorf("brief not updated:\n%s", brief)
	}
	if got := Focus(res.Path); got != "SQL injection only" {
		t.Errorf("focus = %q", got)
	}
	if err := SetFocus(f.main, res.Path, "  "); err != nil || Focus(res.Path) != "" {
		t.Errorf("focus not cleared: %q, %v", Focus(res.Path), err)
	}
	if err := SetFocus(f.main, f.main, "x"); err == nil {
		t.Error("focus set on the main checkout")
	}
}

func TestAddGivesWorktreeConfigAFileOnlyWhenGitReadsIt(t *testing.T) {
	f := newFixture(t)
	path, err := f.repo.Add("plain", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(AdminDir(path), "config.worktree")); err == nil {
		t.Error("config.worktree created although the extension is off")
	}
	git(t, f.main, "config", "extensions.worktreeConfig", "true")
	path, err = f.repo.Add("sparse", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(AdminDir(path), "config.worktree")); err != nil {
		t.Errorf("config.worktree missing with the extension on: %v", err)
	}
}

// Reviews started at the same time in one repository (two terminals) must each
// get their own patchset: nothing may be read from state that another fetch
// overwrites, such as FETCH_HEAD.
func TestParallelReviewsDoNotMixUpPatchsets(t *testing.T) {
	f := newFixture(t)
	const n = 6
	want := map[int]string{}
	for i := 0; i < n; i++ {
		want[1000+i] = f.patchset(t, 1000+i, 1, "change "+strconv.Itoa(i)+"\n")
	}
	type result struct {
		change int
		res    *ReviewResult
		err    error
	}
	out := make(chan result, n)
	for c := range want {
		go func(c int) {
			res, err := f.repo.Review(Change{Number: c}, "", "", false)
			out <- result{c, res, err}
		}(c)
	}
	for i := 0; i < n; i++ {
		r := <-out
		if r.err != nil {
			t.Errorf("change %d: %v", r.change, r.err)
			continue
		}
		if got := git(t, f.main, "rev-parse", "refs/heads/review/"+strconv.Itoa(r.change)); got != want[r.change] {
			t.Errorf("review/%d is at %s, want its own patchset %s", r.change, got, want[r.change])
		}
		brief, _ := os.ReadFile(filepath.Join(AdminDir(r.res.Path), briefFile))
		if !strings.Contains(string(brief), "change "+strconv.Itoa(r.change)+",") {
			t.Errorf("review %d has the wrong task:\n%s", r.change, brief)
		}
	}
}

func TestReviewNamesTheCommitAndWarnsWhenTheBranchIsNotAtIt(t *testing.T) {
	f := newFixture(t)
	sha := f.patchset(t, 4242, 1, "the real change\n")

	// A branch of that name already exists and is somebody else's work.
	other := commit(t, f.main, "other.txt", "unrelated\n")
	git(t, f.main, "branch", "review/4242", other)

	res, err := f.repo.Review(Change{Number: 4242}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Note == "" {
		t.Error("no note although the branch was left on another commit")
	}
	if got := git(t, res.Path, "rev-parse", "HEAD"); got != other {
		t.Errorf("an existing branch must not be moved: HEAD = %s", got)
	}
	brief, _ := os.ReadFile(filepath.Join(AdminDir(res.Path), briefFile))
	for _, want := range []string{sha[:12], "git rev-parse HEAD", "WARNING from airlock", "left alone"} {
		if !strings.Contains(string(brief), want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	// The commit the brief names is really the patchset, and is available to the agent.
	if git(t, res.Path, "log", "-1", "--format=%s", sha[:12]) == "" {
		t.Error("the named commit is not in the repository")
	}
}

func TestReviewResolvesWhichKindOfChangeANumberIs(t *testing.T) {
	f := newFixture(t)
	pr := f.publish(t, "refs/pull/31/head", "a pull request\n")
	mr := f.publish(t, "refs/merge-requests/32/head", "a merge request\n")
	bb := f.publish(t, "refs/pull-requests/34/from", "a Bitbucket pull request\n")
	f.patchset(t, 33, 1, "gerrit 1\n")
	g2 := f.patchset(t, 33, 2, "gerrit 2\n")

	for _, c := range []struct {
		in     Change
		forge  Forge
		branch string
		sha    string
	}{
		{Change{Number: 31}, GitHub, "review/pr-31", pr},
		{Change{Number: 32}, GitLab, "review/mr-32", mr},
		{Change{Number: 34}, Bitbucket, "review/bb-34", bb},
		{Change{Number: 33}, Gerrit, "review/33", g2},
	} {
		res, err := f.repo.Review(c.in, "", "", false)
		if err != nil {
			t.Fatalf("%+v: %v", c.in, err)
		}
		if res.Change.Forge != c.forge || res.Branch != c.branch {
			t.Errorf("%+v resolved to %s on %s", c.in, res.Change.Forge, res.Branch)
		}
		if got := git(t, res.Path, "rev-parse", "HEAD"); got != c.sha {
			t.Errorf("%s: HEAD = %s, want %s", c.branch, got, c.sha)
		}
		brief, _ := os.ReadFile(filepath.Join(AdminDir(res.Path), briefFile))
		if !strings.Contains(string(brief), c.sha[:12]) || !strings.Contains(string(brief), "git rev-parse HEAD") {
			t.Errorf("%s: brief does not name the commit:\n%s", c.branch, brief)
		}
	}
	// Pull and merge requests are told apart from Gerrit changes in what the agent reads.
	prBrief, _ := os.ReadFile(filepath.Join(AdminDir(filepath.Join(f.main, ".worktrees", "review-pr-31")), briefFile))
	for _, want := range []string{"pull request #31", "merge-base", "GitHub"} {
		if !strings.Contains(string(prBrief), want) {
			t.Errorf("pull request brief lacks %q:\n%s", want, prBrief)
		}
	}
	mrBrief, _ := os.ReadFile(filepath.Join(AdminDir(filepath.Join(f.main, ".worktrees", "review-mr-32")), briefFile))
	if !strings.Contains(string(mrBrief), "merge request !32") || !strings.Contains(string(mrBrief), "GitLab") {
		t.Errorf("merge request brief:\n%s", mrBrief)
	}
}

func TestReviewAskForTheKindWhenANumberIsAmbiguous(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "refs/pull/9/head", "pr\n")
	f.publish(t, "refs/merge-requests/9/head", "mr\n")
	f.publish(t, "refs/pull-requests/9/from", "bb\n")
	f.patchset(t, 9, 1, "gerrit\n")

	_, err := f.repo.Review(Change{Number: 9}, "", "", false)
	if err == nil || !strings.Contains(err.Error(), "pr:9") || !strings.Contains(err.Error(), "gerrit") {
		t.Fatalf("an ambiguous number must ask for the kind: %v", err)
	}
	for forge, branch := range map[Forge]string{GitHub: "review/pr-9", GitLab: "review/mr-9", Bitbucket: "review/bb-9", Gerrit: "review/9"} {
		res, err := f.repo.Review(Change{Forge: forge, Number: 9}, "", "", false)
		if err != nil || res.Branch != branch {
			t.Errorf("%s: %+v, %v", forge, res, err)
		}
	}
	if _, err := f.repo.Review(Change{Forge: GitHub, Number: 9, Patchset: 2}, "", "", false); err == nil {
		// a patchset only exists for Gerrit; the resolver must not quietly ignore it
		t.Error("a patchset on a pull request was accepted")
	}
}

func TestPullRequestFollowsItsHeadWithoutLosingWork(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "refs/pull/50/head", "v1\n")
	res, err := f.repo.Review(Change{Forge: GitHub, Number: 50}, "", "", false)
	if err != nil || !res.Created {
		t.Fatalf("%+v, %v", res, err)
	}
	head2 := f.publish(t, "refs/pull/50/head", "v2\n") // the author pushed again (force)
	res, err = f.repo.Review(Change{Number: 50}, "", "", false)
	if err != nil || res.Note != "" || res.Created {
		t.Fatalf("update: %+v, %v", res, err)
	}
	if got := git(t, res.Path, "rev-parse", "HEAD"); got != head2 {
		t.Errorf("HEAD = %s, want the new head %s", got, head2)
	}
	mine := commit(t, res.Path, "change.txt", "my fix\n")
	f.publish(t, "refs/pull/50/head", "v3\n")
	res, err = f.repo.Review(Change{Number: 50}, "", "", false)
	if err != nil || !strings.Contains(res.Note, "local commits") {
		t.Fatalf("local commits must be kept: %+v, %v", res, err)
	}
	if got := git(t, res.Path, "rev-parse", "HEAD"); got != mine {
		t.Errorf("local commit lost: %s", got)
	}
}

func TestReviewUsesTheRemoteAURLPointsAt(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkServer := func(owner string) string {
		dir := filepath.Join(tmp, owner)
		os.MkdirAll(dir, 0o755)
		server := filepath.Join(dir, "widgets.git")
		git(t, tmp, "init", "-q", "--bare", "-b", "main", server)
		return server
	}
	mine, upstream := mkServer("me"), mkServer("acme")
	main := filepath.Join(tmp, "work")
	git(t, tmp, "init", "-q", "-b", "main", main)
	commit(t, main, "a.txt", "one\n")
	git(t, main, "remote", "add", "origin", mine)
	git(t, main, "remote", "add", "upstream", upstream)
	git(t, main, "push", "-q", "origin", "main")
	git(t, main, "push", "-q", "upstream", "main")
	// The pull request exists only upstream, as is usual with a fork.
	clone := filepath.Join(tmp, "c")
	git(t, tmp, "clone", "-q", upstream, clone)
	head := commit(t, clone, "change.txt", "pr\n")
	git(t, clone, "push", "-q", "origin", "HEAD:refs/pull/5/head")

	repo := &Repo{Root: main, Dir: ".worktrees"}
	// A bare number: origin does not have it, upstream does.
	res, err := repo.Review(Change{Number: 5}, "", "", false)
	if err != nil || git(t, res.Path, "rev-parse", "HEAD") != head {
		t.Fatalf("bare number: %+v, %v", res, err)
	}
	// A URL for the upstream repository goes straight to the remote that points at it
	// (matched by path: the host of a local path is empty, as an ssh alias would differ).
	if _, err := repo.Remove("review-pr-5", true); err != nil {
		t.Fatal(err)
	}
	c, _ := ParseChange("https://github.com/acme/widgets/pull/5")
	res, err = repo.Review(c, "", "", false)
	if err != nil || git(t, res.Path, "rev-parse", "HEAD") != head {
		t.Fatalf("URL: %+v, %v", res, err)
	}
	// A URL for a repository none of the remotes is: refuse, saying what to do.
	c, _ = ParseChange("https://github.com/other/thing/pull/5")
	if _, err := repo.Review(c, "", "", false); err == nil || !strings.Contains(err.Error(), "no remote") {
		t.Errorf("unknown repository: %v", err)
	}
	// An explicit remote wins over everything.
	repo.ReviewRemote = "origin"
	if _, err := repo.Review(Change{Forge: GitHub, Number: 5}, "", "other", false); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Errorf("explicit remote ignored: %v", err)
	}
}

func TestTransientGitErrors(t *testing.T) {
	for _, msg := range []string{
		"git worktree: Preparing worktree (new branch 'x')\nfatal: failed to read .git/worktrees/review-1/commondir: Success",
		"git fetch: error: cannot lock ref 'refs/airlock/changes/1/1': is at abc but expected def",
		"git update-ref: fatal: Unable to create '/r/.git/packed-refs.lock': File exists.",
	} {
		if !transient(errors.New(msg)) {
			t.Errorf("not recognised as transient: %s", msg)
		}
	}
	for _, msg := range []string{
		"git fetch: fatal: couldn't find remote ref refs/changes/45/12345/9",
		"git worktree: fatal: 'x' is already checked out at '/r/.worktrees/y'",
		"git ls-remote: fatal: Could not read from remote repository.",
	} {
		if transient(errors.New(msg)) {
			t.Errorf("retried although asking again cannot help: %s", msg)
		}
	}
}

func TestParallelWorktreeAddsAllSucceed(t *testing.T) {
	f := newFixture(t)
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := f.repo.Add("wt"+strconv.Itoa(i), "", "", i%2 == 0)
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("parallel add: %v", err)
		}
	}
	if list, err := f.repo.List(); err != nil || len(list) != n {
		t.Errorf("want %d worktrees, got %d (%v)", n, len(list), err)
	}
}
