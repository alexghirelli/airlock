package worktree

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alexghirelli/airlock/internal/hostgit"
)

// Forge is the kind of server a change under review lives on. All three publish
// what is under review as a git ref, so `git fetch` is all it takes: no API, no
// token on the host beyond what git already uses for the remote.
type Forge string

const (
	Gerrit Forge = "gerrit" // refs/changes/NN/N/P, one ref per patchset
	GitHub Forge = "github" // refs/pull/N/head; Gitea and Forgejo use the same
	GitLab Forge = "gitlab" // refs/merge-requests/N/head
	// Bitbucket is Bitbucket Server / Data Center: refs/pull-requests/N/from.
	// Bitbucket Cloud does not publish pull requests as git refs.
	Bitbucket Forge = "bitbucket"
)

var allForges = []Forge{Gerrit, GitHub, GitLab, Bitbucket}

// Change names something to review: a Gerrit change (and maybe one of its
// patchsets), a pull request or a merge request.
type Change struct {
	// Forge is empty until known; a bare number is matched against what the
	// remote serves.
	Forge    Forge
	Number   int
	Patchset int // Gerrit only; 0 means the latest
	// RepoHost and RepoPath say where a URL put the change ("github.com",
	// "owner/repo"), to find the remote that points there.
	RepoHost, RepoPath string
}

var (
	bareChange  = regexp.MustCompile(`^(?:(pr|mr|bb|gerrit):)?(\d+)(?:[/,.](\d+))?$`)
	refBB       = regexp.MustCompile(`^refs/pull-requests/(\d+)/(?:from|merge)$`)
	urlBB       = regexp.MustCompile(`^(?:/.*?)?/projects/([^/]+)/repos/([^/]+)/pull-requests/(\d+)(?:/|$)`)
	urlBBCloud  = regexp.MustCompile(`/pull-requests/\d+`)
	refChange   = regexp.MustCompile(`^refs/changes/\d\d/(\d+)/(\d+)$`)
	refPull     = regexp.MustCompile(`^refs/pull/(\d+)/(?:head|merge)$`)
	refMR       = regexp.MustCompile(`^refs/merge-requests/(\d+)/(?:head|merge)$`)
	urlPull     = regexp.MustCompile(`^/?(.+?)/pull/(\d+)(?:/|$)`)
	urlMR       = regexp.MustCompile(`^/?(.+?)/(?:-/)?merge_requests/(\d+)(?:/|$)`)
	urlGerrit   = regexp.MustCompile(`^(?:/r)?(?:/c/(.+?))?/\+/(\d+)(?:/(\d+))?(?:/|$)`)
	fragGerrit  = regexp.MustCompile(`^/c/(?:(.+?)/\+/)?(\d+)(?:/(\d+))?`)
	errNoChange = errors.New("use a number (12345), a Gerrit patchset (12345/3), pr:12 or mr:12 to say which kind, or the URL of the change")
)

// ParseChange reads what `airlock review` is given: a number, "12345/3" (Gerrit
// patchset), "pr:12" (GitHub, Gitea, Forgejo), "mr:12" (GitLab), "bb:12"
// (Bitbucket Server), "gerrit:12345", a ref such as refs/pull/12/head, or the URL
// of a Gerrit change, a pull request or a merge request.
func ParseChange(s string) (Change, error) {
	s = strings.TrimSpace(s)
	num := func(v string) int { n, _ := strconv.Atoi(v); return n }
	var c Change
	switch {
	case bareChange.MatchString(s):
		m := bareChange.FindStringSubmatch(s)
		c.Number = num(m[2])
		switch m[1] {
		case "pr":
			c.Forge = GitHub
		case "mr":
			c.Forge = GitLab
		case "bb":
			c.Forge = Bitbucket
		case "gerrit":
			c.Forge = Gerrit
		}
		if m[3] != "" {
			if c.Forge != "" && c.Forge != Gerrit {
				return Change{}, fmt.Errorf("%q: pull and merge requests have no patchsets", s)
			}
			c.Forge, c.Patchset = Gerrit, num(m[3])
		}
	case refChange.MatchString(s):
		m := refChange.FindStringSubmatch(s)
		c = Change{Forge: Gerrit, Number: num(m[1]), Patchset: num(m[2])}
	case refPull.MatchString(s):
		c = Change{Forge: GitHub, Number: num(refPull.FindStringSubmatch(s)[1])}
	case refMR.MatchString(s):
		c = Change{Forge: GitLab, Number: num(refMR.FindStringSubmatch(s)[1])}
	case refBB.MatchString(s):
		c = Change{Forge: Bitbucket, Number: num(refBB.FindStringSubmatch(s)[1])}
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return Change{}, fmt.Errorf("cannot read %q: %w", s, errNoChange)
		}
		c.RepoHost = strings.ToLower(u.Hostname())
		path := strings.TrimSuffix(u.Path, "/")
		switch {
		case urlGerrit.MatchString(path):
			m := urlGerrit.FindStringSubmatch(path)
			c.Forge, c.RepoPath, c.Number, c.Patchset = Gerrit, strings.Trim(m[1], "/"), num(m[2]), num(m[3])
		case fragGerrit.MatchString(u.Fragment):
			m := fragGerrit.FindStringSubmatch(u.Fragment)
			c.Forge, c.RepoPath, c.Number, c.Patchset = Gerrit, strings.Trim(m[1], "/"), num(m[2]), num(m[3])
		case urlBB.MatchString(path):
			m := urlBB.FindStringSubmatch(path)
			c.Forge, c.RepoPath, c.Number = Bitbucket, strings.ToLower(m[1])+"/"+m[2], num(m[3])
		case c.RepoHost == "bitbucket.org" && urlBBCloud.MatchString(path):
			return Change{}, errors.New("Bitbucket Cloud does not publish pull requests as git refs, so there is nothing to fetch. Fetch its source branch and use: airlock wt add <name> -b <branch>")
		case urlMR.MatchString(path):
			m := urlMR.FindStringSubmatch(path)
			c.Forge, c.RepoPath, c.Number = GitLab, strings.Trim(m[1], "/"), num(m[2])
		case urlPull.MatchString(path):
			m := urlPull.FindStringSubmatch(path)
			c.Forge, c.RepoPath, c.Number = GitHub, strings.Trim(m[1], "/"), num(m[2])
		default:
			return Change{}, fmt.Errorf("cannot read a change from %q: %w", s, errNoChange)
		}
	default:
		return Change{}, fmt.Errorf("cannot read a change from %q: %w", s, errNoChange)
	}
	if c.Number <= 0 {
		return Change{}, fmt.Errorf("invalid number in %q", s)
	}
	return c, nil
}

// serverRef is the change's ref on the server.
func (c Change) serverRef() string {
	switch c.Forge {
	case GitHub:
		return fmt.Sprintf("refs/pull/%d/head", c.Number)
	case GitLab:
		return fmt.Sprintf("refs/merge-requests/%d/head", c.Number)
	case Bitbucket:
		return fmt.Sprintf("refs/pull-requests/%d/from", c.Number)
	}
	return fmt.Sprintf("refs/changes/%02d/%d/%d", c.Number%100, c.Number, c.Patchset)
}

func (c Change) gerritPrefix() string {
	return fmt.Sprintf("refs/changes/%02d/%d/", c.Number%100, c.Number)
}

// fetched is where the change is kept locally once fetched: a ref of its own,
// never FETCH_HEAD, which every fetch in the repository overwrites.
func (c Change) fetched() string {
	switch c.Forge {
	case GitHub:
		return fmt.Sprintf("refs/airlock/pull/%d", c.Number)
	case GitLab:
		return fmt.Sprintf("refs/airlock/merge-requests/%d", c.Number)
	case Bitbucket:
		return fmt.Sprintf("refs/airlock/pull-requests/%d", c.Number)
	}
	return fmt.Sprintf("refs/airlock/changes/%d/%d", c.Number, c.Patchset)
}

// key tells reviews of different kinds apart in names.
func (c Change) key() string {
	switch c.Forge {
	case GitHub:
		return "pr-" + strconv.Itoa(c.Number)
	case GitLab:
		return "mr-" + strconv.Itoa(c.Number)
	case Bitbucket:
		return "bb-" + strconv.Itoa(c.Number)
	}
	return strconv.Itoa(c.Number)
}

// Name and Branch are the default worktree and branch for a review.
func (c Change) Name() string   { return "review-" + c.key() }
func (c Change) Branch() string { return "review/" + c.key() }

// anchor remembers which commit the review branch was last set to, so a later
// update can tell whether there are local commits on top of it.
func (c Change) anchor() string { return "refs/airlock/review/" + c.key() }

// What describes the change for people.
func (c Change) What() string {
	switch c.Forge {
	case GitHub:
		return fmt.Sprintf("pull request #%d", c.Number)
	case GitLab:
		return fmt.Sprintf("merge request !%d", c.Number)
	case Bitbucket:
		return fmt.Sprintf("Bitbucket pull request #%d", c.Number)
	}
	if c.Patchset > 0 {
		return fmt.Sprintf("Gerrit change %d, patchset %d", c.Number, c.Patchset)
	}
	return fmt.Sprintf("Gerrit change %d", c.Number)
}

// version is what the branch is set to: a patchset, or the current head.
func (c Change) version() string {
	if c.Forge == Gerrit {
		return fmt.Sprintf("patchset %d", c.Patchset)
	}
	return "the current head"
}

// ---- finding the remote ----

func (r *Repo) remoteURLs() map[string]string {
	out, err := r.git(r.Root, "config", "--get-regexp", `^remote\..+\.url$`)
	urls := map[string]string{}
	if err != nil {
		return urls
	}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		urls[name] = strings.TrimSpace(val)
	}
	return urls
}

// splitRemoteURL gives the host and the repository path of a remote URL, in
// both the "https://host/owner/repo.git" and the "git@host:owner/repo.git" forms.
func splitRemoteURL(u string) (host, path string) {
	if strings.Contains(u, "://") {
		if p, err := url.Parse(u); err == nil {
			host, path = p.Hostname(), p.Path
		}
	} else if !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, ".") && strings.Contains(u, ":") {
		rest := u[strings.LastIndex(u, "@")+1:] // user@host:path
		host, path, _ = strings.Cut(rest, ":")
	} else {
		path = u // a path on this machine
	}
	path = strings.Trim(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), "/")
	return strings.TrimPrefix(strings.ToLower(host), "www."), path
}

// pathMatches compares repository paths ignoring case: forges treat owners and
// project keys that way, and Bitbucket's clone URLs lowercase the key.
func pathMatches(have, want string) bool {
	have, want = strings.ToLower(have), strings.ToLower(want)
	return want != "" && (have == want || strings.HasSuffix(have, "/"+want))
}

// remotesPointingAt finds the remotes of the repository a URL named: same host
// and path first, then the same path alone if only one remote has it (an ssh
// alias for the web host).
func (r *Repo) remotesPointingAt(host, path string) []string {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	var exact, byPath []string
	urls := r.remoteURLs()
	names := make([]string, 0, len(urls))
	for n := range urls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h, p := splitRemoteURL(urls[n])
		switch {
		case path == "" && h == host:
			exact = append(exact, n)
		case pathMatches(p, path) && h == host:
			exact = append(exact, n)
		case pathMatches(p, path):
			byPath = append(byPath, n)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(byPath) == 1 {
		return byPath
	}
	return nil
}

func (r *Repo) remotesFor(c Change) ([]string, error) {
	explicit := r.ReviewRemote
	if explicit == "" && (c.Forge == Gerrit || c.Forge == "") {
		explicit = r.GerritRemote
	}
	if explicit != "" {
		return []string{explicit}, nil
	}
	if c.RepoHost != "" {
		if names := r.remotesPointingAt(c.RepoHost, c.RepoPath); len(names) > 0 {
			return names, nil
		}
		return nil, fmt.Errorf("no remote of this repository points at %s/%s: add one (git remote add), or set git.review_remote", c.RepoHost, c.RepoPath)
	}
	urls := r.remoteURLs()
	var out []string
	for _, n := range []string{"gerrit", "origin", "upstream"} {
		if _, ok := urls[n]; ok {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the repository has no remote to fetch from (gerrit, origin or upstream); set git.review_remote")
	}
	return out, nil
}

// probe asks a remote which kinds of change it has under this number. For
// Gerrit it also reports the patchsets.
func (r *Repo) probe(remote string, c Change) (map[Forge][]int, error) {
	try := allForges
	switch {
	case c.Forge != "":
		try = []Forge{c.Forge}
	case c.Patchset > 0:
		try = []Forge{Gerrit}
	}
	var patterns []string
	for _, f := range try {
		switch f {
		case Gerrit:
			patterns = append(patterns, c.gerritPrefix()+"*")
		case GitHub:
			patterns = append(patterns, fmt.Sprintf("refs/pull/%d/head", c.Number))
		case GitLab:
			patterns = append(patterns, fmt.Sprintf("refs/merge-requests/%d/head", c.Number))
		case Bitbucket:
			patterns = append(patterns, fmt.Sprintf("refs/pull-requests/%d/from", c.Number))
		}
	}
	out, err := r.gitWithRetry(r.Root, networkOpts, append([]string{"ls-remote", remote}, patterns...)...)
	if err != nil {
		return nil, err
	}
	found := map[Forge][]int{}
	for _, line := range strings.Split(out, "\n") {
		_, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch {
		case ref == fmt.Sprintf("refs/pull/%d/head", c.Number):
			found[GitHub] = []int{0}
		case ref == fmt.Sprintf("refs/merge-requests/%d/head", c.Number):
			found[GitLab] = []int{0}
		case ref == fmt.Sprintf("refs/pull-requests/%d/from", c.Number):
			found[Bitbucket] = []int{0}
		case strings.HasPrefix(ref, c.gerritPrefix()):
			if n, err := strconv.Atoi(strings.TrimPrefix(ref, c.gerritPrefix())); err == nil {
				found[Gerrit] = append(found[Gerrit], n)
			}
		}
	}
	return found, nil
}

// resolve settles the kind of change, the patchset and the remote.
func (r *Repo) resolve(c Change) (Change, string, error) {
	if c.Patchset > 0 && c.Forge != "" && c.Forge != Gerrit {
		return c, "", errors.New("only Gerrit changes have patchsets")
	}
	remotes, err := r.remotesFor(c)
	if err != nil {
		return c, "", err
	}
	var tried []string
	for _, remote := range remotes {
		found, err := r.probe(remote, c)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", remote, err))
			continue
		}
		switch len(found) {
		case 0:
			tried = append(tried, remote)
			continue
		case 1:
		default:
			var kinds []string
			for f := range found {
				kinds = append(kinds, string(f))
			}
			sort.Strings(kinds)
			return c, "", fmt.Errorf("%d exists on %s as more than one kind (%s): say which with pr:%d, mr:%d, bb:%d or gerrit:%d",
				c.Number, remote, strings.Join(kinds, ", "), c.Number, c.Number, c.Number, c.Number)
		}
		for f, patchsets := range found {
			c.Forge = f
			if f != Gerrit {
				return c, remote, nil
			}
			sort.Ints(patchsets)
			if c.Patchset == 0 {
				c.Patchset = patchsets[len(patchsets)-1]
				return c, remote, nil
			}
			for _, p := range patchsets {
				if p == c.Patchset {
					return c, remote, nil
				}
			}
			return c, "", fmt.Errorf("change %d has no patchset %d on %s (latest: %d)", c.Number, c.Patchset, remote, patchsets[len(patchsets)-1])
		}
	}
	return c, "", fmt.Errorf("%d not found as a Gerrit change, pull request or merge request on: %s (to use another remote, set git.review_remote)",
		c.Number, strings.Join(tried, ", "))
}

// ---- reviewing ----

// ReviewResult says what Review did.
type ReviewResult struct {
	Path, Branch string
	Change       Change
	Created      bool
	Isolated     bool
	// Note is set when the worktree could not simply be moved to the new version.
	Note string
}

// Review puts a change on a local branch in a worktree and writes the task for
// the agent that will review it. Running it again moves the branch to the
// newest patchset (or the pull request's current head), unless there are local
// changes or commits to keep. A new worktree is isolated when `isolated` says
// so; an existing one stays where it is.
func (r *Repo) Review(c Change, name, branch string, isolated bool) (*ReviewResult, error) {
	c, remote, err := r.resolve(c)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = c.Name()
	}
	if branch == "" {
		branch = c.Branch()
	}
	path, wasIsolated, hasWT := r.Find(name)
	if hasWT {
		isolated = wasIsolated
	} else if path, err = r.PathFor(name, isolated); err != nil {
		return nil, err
	}
	if !r.ok(r.Root, "check-ref-format", "--branch", branch) {
		return nil, fmt.Errorf("invalid branch name %q", branch)
	}
	// Into a ref of its own, never FETCH_HEAD: that file is shared by every fetch
	// in the repository, so two reviews started together would read each other's.
	// The ref also keeps the commit from being garbage collected.
	if _, err := r.gitWithRetry(r.Root, networkOpts, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", remote, "+"+c.serverRef()+":"+c.fetched()); err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", remote, c.serverRef(), err)
	}
	sha, err := r.git(r.Root, "rev-parse", "--verify", c.fetched()+"^{commit}")
	if err != nil {
		return nil, err
	}
	res := &ReviewResult{Path: path, Branch: branch, Change: c, Isolated: isolated}

	switch {
	case !r.branchExists(branch):
		if hasWT {
			return nil, fmt.Errorf("%s already exists and is not on branch %s", path, branch)
		}
		if err := r.worktreeAdd("--lock", "--reason", "airlock", "-b", branch, path, sha); err != nil {
			return nil, err
		}
		res.Created = true
		r.protectConfig(path)
		if _, err := r.git(r.Root, "update-ref", c.anchor(), sha); err != nil {
			return res, err
		}
	case !hasWT:
		if err := r.worktreeAdd("--lock", "--reason", "airlock", path, branch); err != nil {
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
	dir, err := r.taskDir(name, isolated)
	if err != nil {
		return res, err
	}
	return res, r.writeBrief(dir, c, remote, sha, branch, res.Note)
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
		return fmt.Sprintf("branch %s already existed and was not set up by airlock, so it was left alone (%s is %s)", branch, c.version(), shortOr(sha, ""))
	}
	if anchor != tip {
		return fmt.Sprintf("%s has local commits, so it was left alone. To put them on %s: git -C %s rebase --onto %s %s",
			branch, c.version(), path, shortOr(sha, ""), shortOr(anchor, ""))
	}
	dirty, _ := r.git(path, "status", "--porcelain", "--ignore-submodules=all")
	if dirty != "" {
		return fmt.Sprintf("%s has uncommitted changes, so it was left on its old version; commit or discard them and run this again", path)
	}
	if _, err := r.gitWithRetry(path, hostgit.Options{Writes: true}, "reset", "--hard", "--quiet", sha); err != nil {
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

// guessBase is where a pull or merge request most likely branches off: the
// merge-base with the remote's default branch. The target branch is not in git,
// only on the forge, so this can be wrong; the task says so.
func (r *Repo) guessBase(remote, sha string) string {
	for _, ref := range []string{"HEAD", "main", "master"} {
		if mb, err := r.git(r.Root, "merge-base", sha, "refs/remotes/"+remote+"/"+ref); err == nil && mb != "" {
			return mb
		}
	}
	return ""
}

// writeBrief describes the review the agent in this worktree is there to do. It
// names the commit itself, not just the branch: if the branch could not be moved
// (it existed, or has commits of its own), the review still looks at the right
// change. It goes to the worktree's task folder (see tasks.go), not into the
// worktree, where the agent could change what the next start reads.
func (r *Repo) writeBrief(dir string, c Change, remote, sha, branch, note string) error {
	subject, _ := r.git(r.Root, "log", "-1", "--format=%s", sha)
	author, _ := r.git(r.Root, "log", "-1", "--format=%an", sha)
	short := shortOr(sha, "")
	var b strings.Builder
	b.WriteString("# Task for this worktree\n\n")
	if c.Forge == Gerrit {
		fmt.Fprintf(&b, "You are reviewing %s: %q, by %s.\n", c.What(), subject, author)
		fmt.Fprintf(&b, "The patchset is the commit `%s`", short)
		if parent, _ := r.git(r.Root, "rev-parse", "--verify", "-q", sha+"^"); parent != "" {
			fmt.Fprintf(&b, " (parent `%s`): `git show %s` and `git diff %s..%s` are the change", shortOr(parent, ""), short, shortOr(parent, ""), short)
		}
	} else {
		fmt.Fprintf(&b, "You are reviewing %s. Its head commit is `%s`: %q, by %s.\n", c.What(), short, subject, author)
		fmt.Fprintf(&b, "A %s can hold several commits, and its target branch is only known to the forge. ", map[Forge]string{GitHub: "pull request", GitLab: "merge request", Bitbucket: "pull request"}[c.Forge])
		if mb := r.guessBase(remote, sha); mb != "" {
			fmt.Fprintf(&b, "Its likely base is `%s` (merge-base with the remote's default branch), so `git log %s..%s` lists its commits and `git diff %s..%s` is the change; if the target is another branch, say so to the user and use that one",
				shortOr(mb, ""), shortOr(mb, ""), short, shortOr(mb, ""), short)
		} else {
			fmt.Fprintf(&b, "Ask the user which branch it targets (or look it up with the forge's tools), then use `git log <base>..%s` and `git diff <base>..%s`", short, short)
		}
	}
	fmt.Fprintf(&b, ". It should be checked out on the local branch `%s`: run `git rev-parse HEAD` and, if it does not start with `%s`, tell the user before you go on, and review the commit above instead of HEAD.\n", branch, short)
	if note != "" {
		fmt.Fprintf(&b, "\nWARNING from airlock: %s\n", note)
	}
	b.WriteString("\n")
	b.WriteString("- Read the change against its base, and the code around it, before judging it.\n")
	b.WriteString("- Do not push, merge, submit, abandon, approve or vote. Do not edit files unless the user asks; propose a fix first.\n")
	b.WriteString("- Report findings grouped by file: what is wrong, why it matters, and a suggested fix. If there is nothing to report, say so.\n")
	fmt.Fprintf(&b, "- The full description and the other reviewers' comments are on %s: use the tools you have for it (an MCP server, gh, glab), if any.\n", map[Forge]string{Gerrit: "Gerrit", GitHub: "GitHub", GitLab: "GitLab", Bitbucket: "Bitbucket"}[c.Forge])
	return writeTask(dir, reviewFile, []byte(b.String()))
}
