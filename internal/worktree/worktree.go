// Package worktree manages the git worktrees that each get a sandbox of their
// own. It runs git on the host, where the credentials are: nothing here needs a
// token inside a sandbox.
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Repo is the main repository and the directory its worktrees live in.
type Repo struct {
	Root string // main repository, absolute
	Dir  string // worktrees directory relative to Root (".worktrees")
	// GerritRemote names the remote that serves refs/changes/*. Empty means
	// "gerrit" if such a remote exists, otherwise "origin".
	GerritRemote string
}

var nameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// IsolatedDir is the folder, inside the worktrees directory, for worktrees that
// get a sandbox of their own. The project's sandbox does not see it; it does
// see the worktrees next to it.
const IsolatedDir = ".isolated"

// Path is where the named worktree lives when it shares the project's sandbox.
func (r *Repo) Path(name string) (string, error) { return r.PathFor(name, false) }

// PathFor is where the named worktree is created.
func (r *Repo) PathFor(name string, isolated bool) (string, error) {
	if !nameOK.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid worktree name %q (letters, digits, '.', '_' and '-', starting with a letter or digit)", name)
	}
	dir := strings.Trim(r.Dir, "/")
	if dir == "" {
		return "", errors.New("worktrees are disabled (sandbox.worktrees_dir is empty)")
	}
	base := filepath.Join(r.Root, filepath.FromSlash(dir))
	if isolated {
		base = filepath.Join(base, IsolatedDir)
	}
	return filepath.Join(base, name), nil
}

// Find looks for an existing worktree of that name, in either place.
func (r *Repo) Find(name string) (path string, isolated, ok bool) {
	for _, iso := range []bool{false, true} {
		p, err := r.PathFor(name, iso)
		if err != nil {
			return "", false, false
		}
		if _, err := os.Stat(p); err == nil {
			return p, iso, true
		}
	}
	return "", false, false
}

// AdminDir is the worktree's own directory inside the repository's .git
// ("<repo>/.git/worktrees/<name>"), read from the worktree's .git file.
func AdminDir(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	dir = filepath.FromSlash(strings.TrimSpace(dir))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return filepath.Clean(dir)
}

func (r *Repo) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func (r *Repo) ok(dir string, args ...string) bool {
	_, err := r.git(dir, args...)
	return err == nil
}

func (r *Repo) branchExists(branch string) bool {
	return r.ok(r.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

// remoteBranches lists the remotes that have the branch, as "remote/branch".
func (r *Repo) remoteBranches(branch string) []string {
	out, err := r.git(r.Root, "for-each-ref", "--format=%(refname)", "refs/remotes/*/"+branch)
	if err != nil {
		return nil
	}
	var found []string
	for _, ref := range strings.Fields(out) {
		rest, _ := strings.CutPrefix(ref, "refs/remotes/")
		if _, b, ok := strings.Cut(rest, "/"); ok && b == branch && !strings.HasSuffix(rest, "/HEAD") {
			found = append(found, rest)
		}
	}
	return found
}

// Add creates a worktree on a branch. The branch is used as it is when it
// exists, tracked from its remote when only a remote has it, and created from
// `from` (default: HEAD) otherwise.
func (r *Repo) Add(name, branch, from string, isolated bool) (path string, err error) {
	path, err = r.PathFor(name, isolated)
	if err != nil {
		return "", err
	}
	if existing, _, ok := r.Find(name); ok {
		return existing, fmt.Errorf("%s already exists", existing)
	}
	if branch == "" {
		branch = name
	}
	if !r.ok(r.Root, "check-ref-format", "--branch", branch) {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	base := []string{"worktree", "add", "--lock", "--reason", "airlock"}
	switch {
	case r.branchExists(branch):
		if from != "" {
			return "", fmt.Errorf("branch %s already exists; --from only applies to a new branch", branch)
		}
		_, err = r.git(r.Root, append(base, path, branch)...)
	case from == "" && len(r.remoteBranches(branch)) == 1:
		_, err = r.git(r.Root, append(base, "--track", "-b", branch, path, r.remoteBranches(branch)[0])...)
	case from == "" && len(r.remoteBranches(branch)) > 1:
		return "", fmt.Errorf("%s exists on several remotes (%s); say which with --from", branch, strings.Join(r.remoteBranches(branch), ", "))
	default:
		args := append(base, "-b", branch, path)
		if from != "" {
			args = append(args, from)
		}
		_, err = r.git(r.Root, args...)
	}
	if err == nil {
		r.protectConfig(path)
	}
	return path, err
}

// protectConfig gives the worktree an empty config.worktree when git reads one
// (extensions.worktreeConfig), so the sandbox can mount it read-only: it is
// per-worktree configuration that can run code (core.hooksPath, core.fsmonitor).
func (r *Repo) protectConfig(path string) {
	if out, _ := r.git(r.Root, "config", "--type=bool", "--get", "extensions.worktreeConfig"); out != "true" {
		return
	}
	if admin := AdminDir(path); admin != "" {
		f := filepath.Join(admin, "config.worktree")
		if _, err := os.Stat(f); err != nil {
			_ = os.WriteFile(f, nil, 0o644)
		}
	}
}

// Change identifies a Gerrit change, and optionally one of its patchsets.
type Change struct{ Number, Patchset int }

var (
	changeURL   = regexp.MustCompile(`/\+/(\d+)(?:/(\d+))?(?:[/?#]|$)`)
	changeFrag  = regexp.MustCompile(`#/c/(?:[^#]*/\+/)?(\d+)(?:/(\d+))?`)
	changeShort = regexp.MustCompile(`^(\d+)(?:[/,.](\d+))?$`)
	changeRef   = regexp.MustCompile(`^refs/changes/\d\d/(\d+)/(\d+)$`)
)

// ParseChange reads "12345", "12345/3", a Gerrit change URL (with or without a
// patchset) or a refs/changes/NN/12345/3 ref.
func ParseChange(s string) (Change, error) {
	s = strings.TrimSpace(s)
	var m []string
	switch {
	case changeShort.MatchString(s):
		m = changeShort.FindStringSubmatch(s)
	case changeRef.MatchString(s):
		m = changeRef.FindStringSubmatch(s)
	case strings.Contains(s, "://"):
		if m = changeURL.FindStringSubmatch(s); m == nil {
			m = changeFrag.FindStringSubmatch(s)
		}
	}
	if m == nil {
		return Change{}, fmt.Errorf("cannot read a Gerrit change from %q (use 12345, 12345/3 or the change URL)", s)
	}
	c := Change{}
	c.Number, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		c.Patchset, _ = strconv.Atoi(m[2])
	}
	if c.Number <= 0 {
		return Change{}, fmt.Errorf("invalid change number in %q", s)
	}
	return c, nil
}

// Ref is the change's ref on the Gerrit server.
func (c Change) Ref() string {
	return fmt.Sprintf("refs/changes/%02d/%d/%d", c.Number%100, c.Number, c.Patchset)
}

func (c Change) prefix() string {
	return fmt.Sprintf("refs/changes/%02d/%d/", c.Number%100, c.Number)
}

// Name and Branch are the default worktree and branch for a review.
func (c Change) Name() string   { return fmt.Sprintf("review-%d", c.Number) }
func (c Change) Branch() string { return fmt.Sprintf("review/%d", c.Number) }

// anchor remembers which commit the review branch was last set to, so a later
// update can tell whether there are local commits on top of it.
func (c Change) anchor() string { return fmt.Sprintf("refs/airlock/review/%d", c.Number) }

func (r *Repo) gerritRemote() string {
	if r.GerritRemote != "" {
		return r.GerritRemote
	}
	if r.ok(r.Root, "remote", "get-url", "gerrit") {
		return "gerrit"
	}
	return "origin"
}

// latestPatchset asks the server for the highest patchset of a change.
func (r *Repo) latestPatchset(remote string, c Change) (int, error) {
	out, err := r.git(r.Root, "ls-remote", remote, c.prefix()+"*")
	if err != nil {
		return 0, fmt.Errorf("cannot reach remote %q: %w", remote, err)
	}
	best := 0
	for _, line := range strings.Split(out, "\n") {
		_, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(ref, c.prefix())); err == nil && n > best {
			best = n
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("change %d not found on remote %q (if Gerrit is another remote, set git.gerrit_remote in your config)", c.Number, remote)
	}
	return best, nil
}

// ReviewResult says what Review did.
type ReviewResult struct {
	Path, Branch string
	Patchset     int
	Created      bool
	Isolated     bool
	// Note is set when the worktree could not simply be moved to the patchset.
	Note string
}

// Review puts a Gerrit patchset on a local branch in a worktree and writes the
// task for the agent that will review it. Running it again moves the branch to
// the patchset asked for (the latest by default), unless there are local
// changes or commits to keep. A new worktree is isolated when `isolated` says so;
// an existing one stays where it is.
func (r *Repo) Review(c Change, name, branch string, isolated bool) (*ReviewResult, error) {
	if name == "" {
		name = c.Name()
	}
	if branch == "" {
		branch = c.Branch()
	}
	path, wasIsolated, hasWT := r.Find(name)
	if hasWT {
		isolated = wasIsolated
	} else {
		var err error
		if path, err = r.PathFor(name, isolated); err != nil {
			return nil, err
		}
	}
	if !r.ok(r.Root, "check-ref-format", "--branch", branch) {
		return nil, fmt.Errorf("invalid branch name %q", branch)
	}
	remote := r.gerritRemote()
	var err error
	if c.Patchset == 0 {
		if c.Patchset, err = r.latestPatchset(remote, c); err != nil {
			return nil, err
		}
	}
	if _, err := r.git(r.Root, "fetch", "--quiet", "--no-tags", remote, c.Ref()); err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", remote, c.Ref(), err)
	}
	sha, err := r.git(r.Root, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	res := &ReviewResult{Path: path, Branch: branch, Patchset: c.Patchset, Isolated: isolated}

	switch {
	case !r.branchExists(branch):
		if hasWT {
			return nil, fmt.Errorf("%s already exists and is not on branch %s", path, branch)
		}
		if _, err := r.git(r.Root, "worktree", "add", "--lock", "--reason", "airlock", "-b", branch, path, sha); err != nil {
			return nil, err
		}
		res.Created = true
		r.protectConfig(path)
		if _, err := r.git(r.Root, "update-ref", c.anchor(), sha); err != nil {
			return res, err
		}
	case !hasWT:
		if _, err := r.git(r.Root, "worktree", "add", "--lock", "--reason", "airlock", path, branch); err != nil {
			return nil, err
		}
		res.Created = true
		r.protectConfig(path)
		res.Note = r.moveTo(path, branch, c, sha)
		if err := r.anchorIfAt(branch, c, sha); err != nil {
			return res, err
		}
	default:
		res.Note = r.moveTo(path, branch, c, sha)
		if err := r.anchorIfAt(branch, c, sha); err != nil {
			return res, err
		}
	}
	return res, r.writeBrief(path, c, sha, branch)
}

// moveTo moves the branch to sha when that loses nothing, and says why not
// otherwise.
func (r *Repo) moveTo(path, branch string, c Change, sha string) string {
	tip, err := r.git(r.Root, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil || tip == sha {
		return ""
	}
	anchor, _ := r.git(r.Root, "rev-parse", "--verify", "-q", c.anchor())
	if anchor == "" {
		return fmt.Sprintf("branch %s already existed and was not set up by airlock, so it was left alone (new patchset: %s)", branch, shortOr(sha, ""))
	}
	if anchor != tip {
		return fmt.Sprintf("%s has local commits, so it was left alone. To put them on patchset %d: git -C %s rebase --onto %s %s",
			branch, c.Patchset, path, shortOr(sha, ""), shortOr(anchor, ""))
	}
	dirty, _ := r.git(path, "status", "--porcelain")
	if dirty != "" {
		return fmt.Sprintf("%s has uncommitted changes, so it was left on its old patchset; commit or discard them and run this again", path)
	}
	if _, err := r.git(path, "reset", "--hard", "--quiet", sha); err != nil {
		return err.Error()
	}
	return ""
}

// anchorIfAt records sha as the review's base when the branch is exactly there.
func (r *Repo) anchorIfAt(branch string, c Change, sha string) error {
	if tip, err := r.git(r.Root, "rev-parse", "--verify", "refs/heads/"+branch); err == nil && tip == sha {
		_, err = r.git(r.Root, "update-ref", c.anchor(), sha)
		return err
	}
	return nil
}

func shortOr(sha, fallback string) string {
	if sha == "" {
		sha = fallback
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// The task files live in the worktree's own directory inside .git, so they
// never show up in `git status`. Pi reads them (see image/pi-wrapper.sh) every
// time it starts in the worktree.
const (
	briefFile = "airlock-review.md" // written by `wt review`
	focusFile = "airlock-focus.md"  // written by the user
)

// writeBrief describes the review the agent in this worktree is there to do.
func (r *Repo) writeBrief(path string, c Change, sha, branch string) error {
	admin := AdminDir(path)
	if admin == "" {
		return nil
	}
	subject, _ := r.git(r.Root, "log", "-1", "--format=%s", sha)
	author, _ := r.git(r.Root, "log", "-1", "--format=%an", sha)
	parent, _ := r.git(r.Root, "rev-parse", "--verify", "-q", sha+"^")
	var b strings.Builder
	fmt.Fprintf(&b, "# Task for this worktree\n\n")
	fmt.Fprintf(&b, "You are reviewing Gerrit change %d, patchset %d: %q, by %s.\n", c.Number, c.Patchset, subject, author)
	fmt.Fprintf(&b, "The patchset is checked out on the local branch `%s`", branch)
	if parent != "" {
		fmt.Fprintf(&b, "; its parent is `%s`, so `git diff %s..HEAD` is the change", shortOr(parent, ""), shortOr(parent, ""))
	}
	b.WriteString(".\n\n")
	b.WriteString("- Read the change against its parent, and the code around it, before judging it.\n")
	b.WriteString("- Do not push, submit, abandon or vote. Do not edit files unless the user asks; propose a fix first.\n")
	b.WriteString("- Report findings grouped by file: what is wrong, why it matters, and a suggested fix. If there is nothing to report, say so.\n")
	b.WriteString("- The full description and the other reviewers' comments are on Gerrit: use the Gerrit tools you have, if any.\n")
	return os.WriteFile(filepath.Join(admin, briefFile), []byte(b.String()), 0o644)
}

// SetFocus sets what the agent in a worktree concentrates on. Empty text clears it.
func SetFocus(path, text string) error {
	admin := AdminDir(path)
	if admin == "" {
		return fmt.Errorf("%s is not a linked worktree", path)
	}
	f := filepath.Join(admin, focusFile)
	text = strings.TrimSpace(text)
	if text == "" {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(f, []byte("# Focus of this worktree\n\n"+text+"\n"), 0o644)
}

// Focus returns the focus set with SetFocus, without its heading.
func Focus(path string) string {
	b, err := os.ReadFile(filepath.Join(AdminDir(path), focusFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(string(b), "# Focus of this worktree"))
}

// Entry is one linked worktree.
type Entry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"` // empty when detached
	Head   string `json:"head"`
	Dirty  bool   `json:"dirty"`
	// Isolated is true for worktrees that have a sandbox of their own.
	Isolated bool `json:"isolated"`
}

// List returns the linked worktrees (not the main checkout). Names are paths
// relative to the worktrees directory when they live there.
func (r *Repo) List() ([]Entry, error) {
	out, err := r.git(r.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	var cur *Entry
	flush := func() {
		if cur != nil && cur.Path != r.Root {
			st, _ := r.git(cur.Path, "status", "--porcelain")
			cur.Dirty = st != ""
			entries = append(entries, *cur)
		}
		cur = nil
	}
	base := filepath.Join(r.Root, filepath.FromSlash(strings.Trim(r.Dir, "/")))
	isolatedBase := filepath.Join(base, IsolatedDir)
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Entry{Path: val, Name: val, Isolated: true} // elsewhere on disk: its own sandbox
			if rel, err := filepath.Rel(isolatedBase, val); err == nil && !strings.HasPrefix(rel, "..") {
				cur.Name = rel
			} else if rel, err := filepath.Rel(base, val); err == nil && !strings.HasPrefix(rel, "..") {
				cur.Name, cur.Isolated = rel, false
			}
		case "HEAD":
			if cur != nil {
				cur.Head = shortOr(val, "")
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		}
	}
	flush()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Remove deletes a worktree. It refuses uncommitted changes unless force is
// set. The branch is kept.
func (r *Repo) Remove(name string, force bool) (branch string, err error) {
	if _, err := r.PathFor(name, false); err != nil {
		return "", err
	}
	path, _, ok := r.Find(name)
	if !ok {
		return "", fmt.Errorf("no worktree %q (see airlock wt ls)", name)
	}
	branch, _ = r.git(path, "branch", "--show-current")
	if !force {
		if st, _ := r.git(path, "status", "--porcelain"); st != "" {
			return branch, fmt.Errorf("%s has uncommitted changes; commit or discard them, or use --force", name)
		}
	}
	_ = r.ok(r.Root, "worktree", "unlock", path)
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := r.git(r.Root, append(args, path)...); err != nil {
		_ = r.ok(r.Root, "worktree", "lock", "--reason", "airlock", path)
		return branch, err
	}
	return branch, nil
}
