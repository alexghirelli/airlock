package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/version"
)

// cmdRemote keeps a project on a server with git. Only commits travel: push
// sends them, pull fetches the server's branches for review. Nothing is merged
// automatically, and the server never needs access to this machine.
func (e *env) cmdRemote(ctx context.Context) (int, error) {
	sub, host, path, err := parseRemoteArgs(e.a.rest)
	if err != nil {
		return 2, err
	}
	p, err := sandbox.Resolve(path)
	if err != nil {
		return 1, err
	}
	switch sub {
	case "push":
		return e.remotePush(p, host, path)
	case "pull":
		return e.remotePull(p, host)
	}
	return e.remoteOpen(p, host)
}

// clean returns the env with its output stripped of control characters: push and
// pull print what the server and git said. `open` attaches a terminal and keeps
// the raw output.
func (e *env) clean() *env {
	return &env{stdout: cleanWriter{e.stdout}, stderr: cleanWriter{e.stderr}, a: e.a}
}

func (e *env) remotePush(p *sandbox.Project, host, path string) (int, error) {
	e = e.clean()
	branch, err := e.pushBranch(p.Root)
	if err != nil {
		return 1, err
	}
	dirty, err := gitOut(p.Root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return 1, err
	}
	if dirty != "" {
		if !e.a.force {
			return 1, errors.New("uncommitted changes: commit or stash first; only commits travel (--force allows pushing with them left here)")
		}
		e.errf("airlock: warning: uncommitted changes stay on this machine; pushing the committed state of %s", branch)
	}
	if err := e.checkRemoteAirlock(host); err != nil {
		return 1, err
	}
	dir := remoteDir(p.Slug)
	if err := e.serverClean(host, dir); err != nil {
		return 1, err
	}
	if err := sshRun(host, remoteSh(ensureDirScript(dir)), e.stderr, e.stderr); err != nil {
		return 1, fmt.Errorf("could not prepare %s on %s (see above)", dir, shellQuote(host))
	}
	name := remoteName(host, p.Slug)
	if err := ensureGitRemote(p.Root, name, remoteURL(host, p.Slug)); err != nil {
		return 1, err
	}
	push := gitCmd(p.Root, "push", name, pushRefspec(branch))
	push.Stdout, push.Stderr = e.stdout, e.stderr
	if err := push.Run(); err != nil {
		return 1, fmt.Errorf("push to %s failed (git's message is above); if the server has commits you lack, run %s first",
			shellQuote(host), shellJoin("airlock", "remote", "pull", host))
	}
	if err := sshRun(host, remoteSh(checkoutScript(dir, branch)), e.stderr, e.stderr); err != nil {
		return 1, fmt.Errorf("the branch was pushed but the server is still on %s (git's message is above); fix it and rerun", e.serverBranch(host, dir))
	}
	e.errf("airlock: pushed %s to %s:%s", branch, shellQuote(host), dir)
	next := shellJoin(host)
	if path != "" {
		next += " " + shellQuote(path)
	}
	e.errf("airlock: next: airlock remote open %s   (start the agent there)", next)
	e.errf("airlock: next: airlock remote pull %s   (fetch its branches for review)", next)
	return 0, nil
}

// pushBranch is the branch to push: -b names a local branch, otherwise the
// current one, which must exist (a detached HEAD has no branch to push).
func (e *env) pushBranch(root string) (string, error) {
	if b := e.a.branch; b != "" {
		if err := checkBranchName(b); err != nil {
			return "", err
		}
		if _, err := gitOut(root, "rev-parse", "--verify", "-q", "refs/heads/"+b); err != nil {
			return "", fmt.Errorf("no local branch %q", b)
		}
		return b, nil
	}
	out, err := gitOut(root, "symbolic-ref", "-q", "HEAD")
	b, ok := branchFromRef(out)
	if err != nil || !ok {
		return "", errors.New("detached HEAD: check out a branch, or name one with -b")
	}
	if err := checkBranchName(b); err != nil {
		return "", err
	}
	return b, nil
}

// checkRemoteAirlock makes sure `airlock` runs on the server. Exit 127 means it
// is not installed; 255 means ssh could not connect. A different version only
// gets a warning: the server's sandbox then runs its own version.
func (e *env) checkRemoteAirlock(host string) error {
	var out, errb bytes.Buffer
	err := sshRun(host, "airlock version", &out, &errb)
	msg := strings.TrimSpace(safeText(errb.String()))
	if err != nil {
		switch code := exitCode(err); code {
		case -1:
			return err
		case 127:
			return fmt.Errorf("airlock is not installed on %s (or not in its PATH); install it on the server with:\n  %s", shellQuote(host), remoteInstallCmd)
		case 255:
			return fmt.Errorf("cannot reach %s over ssh: %s", shellQuote(host), msg)
		default:
			return fmt.Errorf("airlock on %s failed (exit %d): %s", shellQuote(host), code, msg)
		}
	}
	ver, ok := parseVersionOutput(out.String())
	if !ok {
		return fmt.Errorf("airlock on %s printed no version (last line: %q)", shellQuote(host), lastLine(out.String()))
	}
	if ver != version.Version {
		e.errf("airlock: warning: airlock on %s is %s, this one is %s", shellQuote(host), ver, version.Version)
	}
	return nil
}

// serverClean refuses a push onto a server checkout with uncommitted changes: git
// would refuse the update after the push, and the server would be left behind.
func (e *env) serverClean(host, dir string) error {
	var out, errb bytes.Buffer
	if err := sshRun(host, remoteSh(preflightScript(dir)), &out, &errb); err != nil {
		return fmt.Errorf("cannot check %s on %s: %s", dir, shellQuote(host), strings.TrimSpace(safeText(errb.String())))
	}
	if strings.TrimSpace(out.String()) != "" {
		return fmt.Errorf("the checkout of %s on %s has uncommitted changes, so the push cannot update it. Commit them there (airlock --remote %s shell %s), then airlock remote pull %s and push again",
			dir, shellQuote(host), shellQuote(host), shellQuote(dir), shellQuote(host))
	}
	return nil
}

// serverBranch names the branch the server's checkout is on, for a message.
func (e *env) serverBranch(host, dir string) string {
	var out bytes.Buffer
	if err := sshRun(host, remoteSh(symbolicRefScript(dir)), &out, io.Discard); err != nil {
		return "an unknown branch"
	}
	if b, ok := branchFromRef(out.String()); ok {
		return b
	}
	return "no branch (detached or unborn)"
}

// ensureGitRemote makes sure the remote exists with this URL.
func ensureGitRemote(root, name, url string) error {
	list, err := gitOut(root, "remote")
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Split(list, "\n"), name) {
		_, err := gitOut(root, "remote", "add", name, url)
		return err
	}
	cur, err := gitOut(root, "remote", "get-url", name)
	if err != nil || cur == url {
		return err
	}
	_, err = gitOut(root, "remote", "set-url", name, url)
	return err
}

func (e *env) remotePull(p *sandbox.Project, host string) (int, error) {
	e = e.clean()
	name := remoteName(host, p.Slug)
	if err := ensureGitRemote(p.Root, name, remoteURL(host, p.Slug)); err != nil {
		return 1, err
	}
	before, err := remoteRefs(p.Root, name)
	if err != nil {
		return 1, err
	}
	fetch := gitCmd(p.Root, "fetch", "-q", "--no-tags", "--no-recurse-submodules", "--prune", name)
	fetch.Stderr = e.stderr
	if err := fetch.Run(); err != nil {
		return 1, fmt.Errorf("fetch from %s failed (git's message is above); if the project is not on the server yet, run %s",
			shellQuote(host), shellJoin("airlock", "remote", "push", host))
	}
	after, err := remoteRefs(p.Root, name)
	if err != nil {
		return 1, err
	}
	moved := movedBranches(before, after)
	if len(moved) == 0 {
		e.errf("airlock: no branch moved on %s", shellQuote(host))
	}
	for _, m := range moved {
		if strings.HasPrefix(m.Name, "-") || strings.HasPrefix(m.Name, "+") {
			e.errf("airlock: skipped %q: names starting with - or + are not looked at", m.Name)
			continue
		}
		ref := name + "/" + m.Name
		if m.Old == "" {
			e.errf("airlock: %s: new branch at %s", m.Name, shortSHA(m.New))
		} else {
			e.errf("airlock: %s: %s..%s", m.Name, shortSHA(m.Old), shortSHA(m.New))
		}
		if localBranchExists(p.Root, m.Name) {
			e.errf("airlock:   look: %s", shellJoin("git", "log", "--oneline", m.Name+".."+ref))
		} else {
			e.errf("airlock:   look: %s", shellJoin("git", "log", "--oneline", ref))
		}
		e.errf("airlock:   merge after review: %s", shellJoin("git", "merge", ref))
	}
	e.errf("airlock: nothing was merged. Review the code first, then run airlock audit after merging.")
	e.errf("airlock: uncommitted work on %s does not travel: commit it inside the sandbox there (%s), then pull again",
		shellQuote(host), shellJoin("airlock", "--remote", host, "shell", remoteDir(p.Slug)))
	return 0, nil
}

// remoteRefs reads the branches fetched from a remote, by name.
func remoteRefs(root, name string) (map[string]string, error) {
	out, err := gitOut(root, "for-each-ref", "--format=%(objectname) %(refname)", "refs/remotes/"+name+"/")
	if err != nil {
		return nil, err
	}
	return parseRemoteRefs(out, name), nil
}

func localBranchExists(root, branch string) bool {
	_, err := gitOut(root, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	return err == nil
}

// remoteOpen starts or attaches the project's sandbox on the server. It is
// `airlock --remote <host> airlock-remote/<slug>`: the path is relative to the
// home directory, where the server's login shell starts, so it names the folder
// push wrote to. The sandbox there uses the server's own configuration: the
// mounts, environment and MCP servers of this machine do not exist there.
func (e *env) remoteOpen(p *sandbox.Project, host string) (int, error) {
	a := *e.a
	a.remote = host
	return runRemote(&env{stdout: e.stdout, stderr: e.stderr, a: &a}, []string{remoteDir(p.Slug)}), nil
}

// sshPath is the ssh client. AIRLOCK_SSH replaces it; the tests use a fake one.
func sshPath() (string, error) {
	if p := os.Getenv("AIRLOCK_SSH"); p != "" {
		return p, nil
	}
	return exec.LookPath("ssh")
}

// sshRun runs one command on host, with its stdout and stderr going to the given writers.
func sshRun(host, command string, stdout, stderr io.Writer) error {
	ssh, err := sshPath()
	if err != nil {
		return errors.New("the remote commands need the ssh client on this machine")
	}
	cmd := exec.Command(ssh, host, command)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// exitCode is the exit status of a failed command, or -1 when it did not run.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// gitCmd is git in dir. GIT_SSH_COMMAND follows AIRLOCK_SSH, so git's own ssh
// is the same fake one the tests give airlock.
func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if ssh := os.Getenv("AIRLOCK_SSH"); ssh != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+shellQuote(ssh))
	}
	return cmd
}

// gitOut runs git in dir and returns its trimmed stdout.
func gitOut(dir string, args ...string) (string, error) {
	cmd := gitCmd(dir, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(safeText(errb.String()))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}
