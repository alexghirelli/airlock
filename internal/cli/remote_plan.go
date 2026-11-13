package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// The pure parts of `airlock remote push|pull|open`: names, the shell lines run
// on the server, and the argument checks. Nothing in this file runs a command.

// remoteInstallCmd is the install line for a server that lacks airlock. It is run
// on the server.
const remoteInstallCmd = "curl -fsSL https://raw.githubusercontent.com/alexghirelli/airlock/main/install.sh | sh"

// remoteDir is where a project lives on the server, relative to the home
// directory. ssh starts a command in the home directory, and a relative path
// given to airlock there resolves against the same place, so `open` can use
// this path as it is.
func remoteDir(slug string) string { return "airlock-remote/" + slug }

// remoteName is the git remote on this machine for one project on one server.
// It carries the project's slug, so the project root and each of its worktrees
// (which have folders of their own on the server) never point one shared remote
// at different places.
func remoteName(host, slug string) string { return "airlock-" + sanitizeHost(host) + "-" + slug }

// remoteURL is the git address of a project on a server (scp-like syntax, so git
// runs ssh for it).
func remoteURL(host, slug string) string { return host + ":" + remoteDir(slug) }

// sanitizeHost makes a host safe inside a remote name. Characters outside
// [A-Za-z0-9_-] become "-"; when that changes anything, a short hash of the
// original host is appended, so "me@box" and "me-box" never share a name.
func sanitizeHost(host string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '-'
	}, host)
	if clean == host {
		return clean
	}
	sum := sha256.Sum256([]byte(host))
	return clean + "-" + hex.EncodeToString(sum[:])[:8]
}

// ensureDirScript prepares the project folder on the server. git init runs
// every time: in a repository it changes nothing, and it gives the folder a .git
// of its own even when the server's home is itself a repository (a dotfiles
// repo, say), which is then never touched.
func ensureDirScript(dir string) string {
	d := shellQuote(dir)
	return "mkdir -p " + d + " && cd " + d + " && git init -q && git config receive.denyCurrentBranch updateInstead"
}

// remoteSh runs a script on the server under sh. The login shell may be fish or
// another shell whose syntax the scripts do not use.
func remoteSh(script string) string { return "sh -c " + shellQuote(script) }

// preflightScript prints the uncommitted changes of the server checkout (none
// when the folder does not exist yet).
func preflightScript(dir string) string {
	return "if [ -d " + shellQuote(dir+"/.git") + " ]; then cd " + shellQuote(dir) + " && git status --porcelain -uno; fi"
}

// symbolicRefScript prints the branch the server checkout is on.
func symbolicRefScript(dir string) string {
	return "cd " + shellQuote(dir) + " && git symbolic-ref -q HEAD"
}

// checkoutScript makes the server checkout show the pushed branch. On a fresh
// repository HEAD is unborn, and the checkout creates it. The "--" stops git
// from reading the branch as a path.
func checkoutScript(dir, branch string) string {
	return "cd " + shellQuote(dir) + " && git checkout -q " + shellQuote(branch) + " --"
}

// pushRefspec pushes a local branch to the branch of the same name on the
// server, spelled out in full so that no name is read as a pattern.
func pushRefspec(branch string) string {
	return "refs/heads/" + branch + ":refs/heads/" + branch
}

// checkBranchName refuses names that git would read as options or as refspec
// modifiers.
func checkBranchName(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "+") {
		return fmt.Errorf("branch %q cannot be pushed: names starting with - or + are refused", b)
	}
	return nil
}

// lastLine is the last non-empty line of a command's output. A server may print
// a banner before the command's own output, so the answer is always the last line.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// branchFromRef turns the output of `git symbolic-ref -q HEAD` into a branch name.
func branchFromRef(out string) (string, bool) {
	return strings.CutPrefix(lastLine(out), "refs/heads/")
}

// parseVersionOutput reads what `airlock version` prints ("airlock 0.1.0") from
// the last line of its output.
func parseVersionOutput(out string) (string, bool) {
	return strings.CutPrefix(lastLine(out), "airlock ")
}

// parseRemoteRefs reads `git for-each-ref --format='%(objectname) %(refname)'`
// over refs/remotes/<remote>/ into branch name -> commit.
func parseRemoteRefs(out, remote string) map[string]string {
	prefix := "refs/remotes/" + remote + "/"
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		name := strings.TrimPrefix(ref, prefix)
		if !ok || name == ref || name == "HEAD" {
			continue
		}
		refs[name] = sha
	}
	return refs
}

// movedBranch is a branch the server has that moved since the last pull. Old is
// empty for a branch that was not there before.
type movedBranch struct{ Name, Old, New string }

// movedBranches lists the branches whose commit changed between two reads, by name.
func movedBranches(before, after map[string]string) []movedBranch {
	var moved []movedBranch
	for name, sha := range after {
		if before[name] != sha {
			moved = append(moved, movedBranch{Name: name, Old: before[name], New: sha})
		}
	}
	slices.SortFunc(moved, func(a, b movedBranch) int { return strings.Compare(a.Name, b.Name) })
	return moved
}

// shortSHA shortens a commit id for display.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// shellJoin quotes each word for a shell. The hints airlock prints are meant to
// be pasted, and a branch name from the server must not turn into a command.
func shellJoin(words ...string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = shellQuote(w)
	}
	return strings.Join(q, " ")
}

// safeText drops control characters: text from ssh and git must not move the
// cursor or hide lines on this terminal.
func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f)) {
			return r
		}
		return -1 // C0, DEL and C1 (U+009B is a one-character CSI)
	}, s)
}

// cleanWriter applies safeText to what is written through it, for the output of
// the git and ssh commands that push and pull run.
type cleanWriter struct{ w io.Writer }

func (c cleanWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		if b == '\n' || b == '\t' || (b >= 0x20 && b != 0x7f) {
			out = append(out, b)
		}
	}
	if _, err := c.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// parseRemoteArgs reads `remote <sub> <host> [path]`.
func parseRemoteArgs(rest []string) (sub, host, path string, err error) {
	const usage = "usage: airlock remote push|pull|open <host> [path]"
	if len(rest) == 0 {
		return "", "", "", errors.New(usage)
	}
	sub = rest[0]
	switch sub {
	case "push", "pull", "open":
	default:
		return "", "", "", fmt.Errorf("unknown remote command %q\n%s", sub, usage)
	}
	if len(rest) < 2 {
		return "", "", "", errors.New(usage)
	}
	if len(rest) > 3 {
		return "", "", "", fmt.Errorf("unexpected argument %q\n%s", rest[3], usage)
	}
	host = rest[1]
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\n:/\\") {
		return "", "", "", fmt.Errorf("invalid remote host %q (a host name or an ssh alias, as ssh takes it)", host)
	}
	if len(rest) == 3 {
		path = rest[2]
	}
	return sub, host, path, nil
}
