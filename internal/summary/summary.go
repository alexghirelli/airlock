// Package summary says what a worktree's agent changed, before anyone trusts or
// merges it: the commits past the base, the files, what is not committed yet,
// and the task and focus the worktree was given. The `summary` command prints
// it; the agent handoff writes it as a briefing. Collect only reads: it runs
// git, and never writes to the repository or to airlock's state.
//
// The checkout is written by the agent, so everything read from it is data:
// git is run with its hooks, fsmonitor and textconv filters off, and the text
// the renderers print is made safe for a terminal or for another agent.
package summary

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alexghirelli/airlock/internal/worktree"
)

// Limits on what the data keeps. The text and Markdown show fewer.
const (
	maxCommits   = 50
	maxUntracked = 50
)

// Summary is what one checkout changed. JSON names are stable: the agent
// handoff and scripts read them.
type Summary struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`   // "" when detached
	Head    string `json:"head"`     // short hash
	BaseRef string `json:"base_ref"` // "" when no base was found
	Base    string `json:"base"`     // short hash of merge-base(HEAD, BaseRef)

	Commits     []Commit `json:"commits"` // the newest maxCommits past the base
	CommitCount int      `json:"commit_count"`
	Files       []File   `json:"files"` // tracked changes since the base, committed or not

	Uncommitted    int      `json:"uncommitted"` // entries in git status, untracked excluded
	Untracked      []string `json:"untracked"`   // the first maxUntracked
	UntrackedCount int      `json:"untracked_count"`

	Focus  string   `json:"focus"`  // what the user said the agent concentrates on
	Review bool     `json:"review"` // a review brief from `airlock review` exists
	Notes  []string `json:"notes"`
	// Audit is the project-wide result of `airlock audit`, set by the caller.
	// Nil when it could not be read.
	Audit *Audit `json:"audit"`
}

// Commit is one commit past the base.
type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// File is one tracked file that differs from the base.
type File struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // renames and copies
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
	Status  string `json:"status"` // M, A, D, R, C, T
}

// Audit is the project-wide result of `airlock audit`: changes to
// host-executable files since the last `airlock audit --accept`. Baseline is
// false until one has been recorded, and then Changes is empty.
type Audit struct {
	Baseline bool     `json:"baseline"`
	Changes  []string `json:"changes"`
}

// Collect reads the checkout at path. name labels it; empty means its folder name.
// tasks is the worktree's task folder (see worktree.TaskDir), where its focus and
// review come from; empty when it has none, as the main checkout.
func Collect(path, name, tasks string) (*Summary, error) {
	if name == "" {
		name = filepath.Base(path)
	}
	s := &Summary{Name: name, Path: path, Commits: []Commit{}, Files: []File{}, Untracked: []string{}, Notes: []string{}}
	head, err := gitOut(path, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("%s: nothing to summarise (no commit yet?): %w", path, err)
	}
	s.Head = head
	// A detached HEAD fails here and leaves Branch empty.
	s.Branch, _ = gitOut(path, "symbolic-ref", "--quiet", "--short", "HEAD")
	remote := ""
	if s.Branch != "" {
		remote, _ = gitOut(path, "config", "--get", "branch."+s.Branch+".remote")
	}
	upstream, _ := gitOut(path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	originHead, _ := gitOut(path, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	upstream = usableUpstream(s.Branch, remote, upstream, originHead)

	diffBase := "HEAD"
	ref, mb, found := findBase(path, s.Branch, upstream, originHead)
	switch {
	case mb != "":
		s.BaseRef = ref
		s.Base, _ = gitOut(path, "rev-parse", "--short", mb)
		diffBase = mb
		if err := s.collectCommits(path, mb); err != nil {
			return nil, err
		}
	case len(found) > 0:
		s.Notes = append(s.Notes, fmt.Sprintf("no merge-base between HEAD and %s (unrelated histories or a shallow clone): only uncommitted changes are summarised", strings.Join(found, ", ")))
	default:
		s.Notes = append(s.Notes, "no base branch found (no upstream, origin/HEAD, main or master): only uncommitted changes are summarised")
	}

	if err := s.collectFiles(path, diffBase); err != nil {
		return nil, err
	}
	st, err := git(path, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	s.Uncommitted = countStatus(st)
	ut, err := git(path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	list := splitNUL(ut)
	s.UntrackedCount = len(list)
	if len(list) > maxUntracked {
		list = list[:maxUntracked]
	}
	s.Untracked = append(s.Untracked, list...)

	s.Focus = worktree.Focus(tasks)
	s.Review = worktree.Brief(tasks) != ""
	return s, nil
}

// usableUpstream drops an upstream that is the branch's own copy on its remote
// (a pushed feature branch): as a base it would be the branch's own tip, so
// nothing but the uncommitted changes would show. The default branch keeps its
// upstream. `push -u origin feature:other` is not recognised: its upstream has
// another name, so it is used as it is.
func usableUpstream(branch, remote, upstream, originHead string) string {
	if upstream == "" || isDefaultBranch(branch, originHead) {
		return upstream
	}
	if remote != "" && remote != "." && strings.HasPrefix(upstream, remote+"/") && strings.TrimPrefix(upstream, remote+"/") == branch {
		return ""
	}
	return upstream
}

func isDefaultBranch(branch, originHead string) bool {
	return branch != "" && (branch == "main" || branch == "master" || branch == strings.TrimPrefix(originHead, "origin/") && originHead != "")
}

// findBase returns the first base candidate that exists and has a merge-base
// with HEAD, and that merge-base. Both are empty when there is none. found lists
// the candidates that exist, so a caller can tell "no candidate" from "no
// merge-base with any of them".
func findBase(path, branch, upstream, originHead string) (ref, base string, found []string) {
	for _, r := range baseRefs(branch, upstream, originHead) {
		if _, err := gitOut(path, "rev-parse", "--verify", "--quiet", r+"^{commit}"); err != nil {
			continue
		}
		found = append(found, r)
		mb, err := gitOut(path, "merge-base", "HEAD", r)
		if err != nil || mb == "" {
			continue
		}
		return r, mb, found
	}
	return "", "", found
}

// baseRefs lists where the branch's changes may start, most specific first: its
// upstream, then the remote's default branch, then the usual names. Those names
// skip the branch itself, which would make it its own base.
func baseRefs(branch, upstream, originHead string) []string {
	var refs []string
	if upstream != "" {
		refs = append(refs, upstream)
	}
	for _, r := range []string{originHead, "origin/main", "origin/master", "main", "master"} {
		if r == "" || strings.TrimPrefix(r, "origin/") == branch || contains(refs, r) {
			continue
		}
		refs = append(refs, r)
	}
	return refs
}

func (s *Summary) collectCommits(path, base string) error {
	rng := base + "..HEAD"
	out, err := git(path, "log", "--no-color", "-n", strconv.Itoa(maxCommits), "--format=%h%x1f%s", rng, "--")
	if err != nil {
		return err
	}
	s.Commits = append(s.Commits, parseLog(out)...)
	n, err := gitOut(path, "rev-list", "--count", rng)
	if err != nil {
		return err
	}
	s.CommitCount, _ = strconv.Atoi(n)
	return nil
}

// collectFiles reads the file list twice, with the same arguments except the
// output format, so that renames found by one are found by the other.
func (s *Summary) collectFiles(path, base string) error {
	common := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M"}
	nums, err := git(path, append(append([]string{}, common...), "--numstat", "-z", base)...)
	if err != nil {
		return err
	}
	stats, err := git(path, append(append([]string{}, common...), "--name-status", "-z", base)...)
	if err != nil {
		return err
	}
	files, err := parseNumstat(nums)
	if err != nil {
		return err
	}
	status := parseNameStatus(stats)
	for i := range files {
		files[i].Status = status[files[i].Path]
		if files[i].Status == "" {
			return fmt.Errorf("git diff: no status for %q", files[i].Path)
		}
	}
	s.Files = append(s.Files, files...)
	return nil
}

// git runs git on the repository at dir and returns its output as it is: -z
// output must not be trimmed. The checkout is agent-writable, so the options
// that would run code from it (hooks, fsmonitor) are off.
func git(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir,
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "diff.relative=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = gitEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := git(dir, args...)
	return strings.TrimSpace(out), err
}

// gitEnv drops the variables that point git at another repository, another
// object store or other configuration (set when airlock runs inside a hook or
// a rebase, or by the environment of whoever runs it), so -C alone picks the
// checkout that is read.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR", "GIT_NAMESPACE",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_QUARANTINE_PATH":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
