// Package worktree manages the git worktrees that each get a sandbox of their
// own. It runs git on the host, where the credentials are: nothing here needs a
// token inside a sandbox.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alexghirelli/airlock/internal/hostgit"
)

// Repo is the main repository and the directory its worktrees live in.
type Repo struct {
	Root string // main repository, absolute
	Dir  string // worktrees directory relative to Root (".worktrees")
	// ReviewRemote names the remote that serves what airlock review fetches
	// (Gerrit changes, pull requests, merge requests). GerritRemote does the same
	// for Gerrit alone. With neither set, the remotes "gerrit", "origin" and
	// "upstream" are tried in that order, or the one a change URL points at.
	ReviewRemote, GerritRemote string
	// TasksOf is the host folder of a worktree's task files, given its name and
	// whether it is isolated. The caller sets it (see sandbox.WorktreeTasks): the
	// folder depends on the sandbox that serves the worktree.
	TasksOf func(name string, isolated bool) (string, error)
}

// taskDir is the task folder of the named worktree, as set by TasksOf.
func (r *Repo) taskDir(name string, isolated bool) (string, error) {
	if r.TasksOf == nil {
		return "", errors.New("no folder for the task files of worktrees")
	}
	return r.TasksOf(name, isolated)
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
	return r.gitWithRetry(dir, hostgit.Options{}, args...)
}

// gitWithRetry is git with options, asking again while git reports a transient
// failure.
func (r *Repo) gitWithRetry(dir string, o hostgit.Options, args ...string) (string, error) {
	var out string
	var err error
	for attempt := 1; ; attempt++ {
		if out, err = r.gitWith(dir, o, args...); err == nil || attempt == 10 || !transient(err) {
			return out, err
		}
		// Another airlock (or git) is writing in the same repository, as when two
		// reviews start together: back off a little and ask again.
		time.Sleep(time.Duration(attempt*30+rand.Intn(50)) * time.Millisecond)
	}
}

// transient recognises the failures git reports when it meets another process
// halfway through writing a lock file or a new worktree's directory. None of
// them has changed anything yet, so asking again is safe.
func transient(err error) bool {
	msg := err.Error()
	for _, s := range []string{"failed to read .git/worktrees/", "Unable to create", "cannot lock ref", "Another git process", "unable to lock", ".lock': File exists"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// withLock serialises the changes airlock makes to the repository's worktrees.
// `git worktree add` is not safe to run twice at once in one repository: the
// second can read the first's directory half written and fail, after it has
// already created its branch. If the lock cannot be had in 30 seconds the work
// goes ahead without it.
func (r *Repo) withLock(fn func() error) error {
	f, err := os.OpenFile(filepath.Join(r.Root, ".git", "airlock.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fn()
	}
	defer f.Close()
	for deadline := time.Now().Add(30 * time.Second); lockFile(f) != nil && time.Now().Before(deadline); {
		time.Sleep(time.Duration(20+rand.Intn(30)) * time.Millisecond)
	}
	defer unlockFile(f)
	return fn()
}

// worktreeAdd runs `git worktree add` under the lock, and never retries: a
// failed add may already have created the branch.
func (r *Repo) worktreeAdd(args ...string) error {
	return r.withLock(func() error {
		_, err := r.gitOnce(r.Root, append([]string{"worktree", "add"}, args...)...)
		return err
	})
}

// networkOpts is for the calls that talk to a remote: they may take longer.
var networkOpts = hostgit.Options{Timeout: 10 * time.Minute}

// gitOnce runs git in dir, a checkout of the repository, through hostgit: git
// never finds the repository by itself from dir.
func (r *Repo) gitOnce(dir string, args ...string) (string, error) {
	return r.gitWith(dir, hostgit.Options{}, args...)
}

// gitWith is gitOnce with options. Options.Writes is set for the calls that
// change a worktree's index or HEAD.
func (r *Repo) gitWith(dir string, o hostgit.Options, args ...string) (string, error) {
	loc, err := hostgit.Open(r.Root, dir)
	if err != nil {
		return "", err
	}
	out, err := loc.Output(context.Background(), o, args...)
	return strings.TrimSpace(out), err
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
	base := []string{"--lock", "--reason", "airlock"}
	switch {
	case r.branchExists(branch):
		if from != "" {
			return "", fmt.Errorf("branch %s already exists; --from only applies to a new branch", branch)
		}
		err = r.worktreeAdd(append(base, path, branch)...)
	case from == "" && len(r.remoteBranches(branch)) == 1:
		err = r.worktreeAdd(append(base, "--track", "-b", branch, path, r.remoteBranches(branch)[0])...)
	case from == "" && len(r.remoteBranches(branch)) > 1:
		return "", fmt.Errorf("%s exists on several remotes (%s); say which with --from", branch, strings.Join(r.remoteBranches(branch), ", "))
	default:
		args := append(base, "-b", branch, path)
		if from != "" {
			args = append(args, from)
		}
		err = r.worktreeAdd(args...)
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
			st, _ := r.git(cur.Path, "status", "--porcelain", "--ignore-submodules=all")
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
		if st, _ := r.git(path, "status", "--porcelain", "--ignore-submodules=all"); st != "" {
			return branch, fmt.Errorf("%s has uncommitted changes; commit or discard them, or use --force", name)
		}
	}
	err = r.withLock(func() error {
		_ = r.ok(r.Root, "worktree", "unlock", path)
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := r.git(r.Root, append(args, path)...); err != nil {
			_ = r.ok(r.Root, "worktree", "lock", "--reason", "airlock", path)
			return err
		}
		return nil
	})
	return branch, err
}

// Stale returns the linked worktrees whose folder is gone from the disk (deleted
// by hand). airlock locks its worktrees, so `git worktree prune` leaves them be.
func (r *Repo) Stale() ([]Entry, error) {
	list, err := r.List()
	if err != nil {
		return nil, err
	}
	var stale []Entry
	for _, w := range list {
		if _, err := os.Stat(w.Path); errors.Is(err, os.ErrNotExist) {
			stale = append(stale, w)
		}
	}
	return stale, nil
}

// PruneStale forgets the worktrees Stale returns. Their branches are kept.
func (r *Repo) PruneStale(stale []Entry) error {
	return r.withLock(func() error {
		for _, w := range stale {
			_ = r.ok(r.Root, "worktree", "unlock", w.Path)
		}
		_, err := r.git(r.Root, "worktree", "prune")
		return err
	})
}
