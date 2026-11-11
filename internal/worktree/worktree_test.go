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
	good := map[string]Change{
		"12345":   {12345, 0},
		"12345/3": {12345, 3},
		"12345,3": {12345, 3},
		"https://gerrit.example.org/r/c/mediawiki/core/+/12345":       {12345, 0},
		"https://gerrit.example.org/r/c/mediawiki/core/+/12345/7":     {12345, 7},
		"https://gerrit.example.org/r/c/mediawiki/core/+/12345/7?x=y": {12345, 7},
		"https://gerrit.example.org/#/c/12345/2":                      {12345, 2},
		"https://gerrit.example.org/#/c/mediawiki/core/+/12345/2":     {12345, 2},
		"refs/changes/45/12345/9":                                     {12345, 9},
		" 7 ":                                                         {7, 0},
	}
	for in, want := range good {
		got, err := ParseChange(in)
		if err != nil || got != want {
			t.Errorf("ParseChange(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "0", "https://example.org/", "12345/x", "-3"} {
		if c, err := ParseChange(in); err == nil {
			t.Errorf("ParseChange(%q) = %v, want an error", in, c)
		}
	}
}

func TestChangeRef(t *testing.T) {
	if got := (Change{12345, 3}).Ref(); got != "refs/changes/45/12345/3" {
		t.Errorf("ref = %q", got)
	}
	if got := (Change{7, 1}).Ref(); got != "refs/changes/07/7/1" {
		t.Errorf("ref = %q", got)
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
	git(t, clone, "push", "-q", "origin", "HEAD:"+Change{change, ps}.Ref())
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
	if res.Patchset != 2 || !res.Created || res.Branch != "review/12345" {
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
	if err := SetFocus(res.Path, "SQL injection only"); err != nil {
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
	if err := SetFocus(res.Path, "  "); err != nil || Focus(res.Path) != "" {
		t.Errorf("focus not cleared: %q, %v", Focus(res.Path), err)
	}
	if err := SetFocus(f.main, "x"); err == nil {
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
