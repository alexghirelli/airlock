package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/version"
)

func TestParseArgsRemote(t *testing.T) {
	a, err := parseArgs([]string{"remote", "push", "box", "/p", "-b", "feat", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if a.command != "remote" || a.remote != "" || !reflect.DeepEqual(a.rest, []string{"push", "box", "/p"}) || a.branch != "feat" || !a.force {
		t.Errorf("remote push: %+v", a)
	}
	a, err = parseArgs([]string{"--remote", "box", "exec", "--", "ls"})
	if err != nil || a.command != "exec" || a.remote != "box" {
		t.Errorf("--remote still runs commands on the host: %+v, %v", a, err)
	}
}

// serverShell is the fake ssh. The host is ignored, except for the names the
// tests use: "unreachable" fails like ssh does, "nostub" has no airlock, and
// "oldstub" has an older one. The last argument is the command, run under sh in
// the fake server's home, where git-receive-pack and git-upload-pack are found
// through git's exec path.
const serverShell = `#!/bin/sh
case " $* " in
*" unreachable "*) echo 'ssh: connect to host unreachable port 22: Connection refused' >&2; exit 255 ;;
esac
for last; do :; done
cd '%[1]s' || exit 255
case " $* " in
*" nostub "*) PATH='%[2]s':"$PATH" ;;
*" oldstub "*) PATH='%[4]s':'%[2]s':"$PATH" ;;
*) PATH='%[3]s':'%[2]s':"$PATH" ;;
esac
HOME='%[1]s'
export HOME PATH
exec sh -c "$last"
`

// isolateGit keeps git out of the real configuration and out of any repository
// the test process was started in. A GIT_DIR inherited from a hook or a rebase
// would send these tests' commands into the wrong repository.
func isolateGit(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"} {
		if v, ok := os.LookupEnv(k); ok {
			k, v := k, v
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.org")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.org")
}

// remoteFixture is a fake server (a home folder under the test's temp dir) and
// the fake ssh that reaches it.
type remoteFixture struct {
	t          *testing.T
	tmp        string
	serverHome string
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as ssh")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateGit(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fx := &remoteFixture{t: t, tmp: tmp, serverHome: filepath.Join(tmp, "server-home")}
	home := filepath.Join(tmp, "home")
	stubs := filepath.Join(tmp, "stubs")
	oldStubs := filepath.Join(tmp, "old-stubs")
	sshDir := filepath.Join(tmp, "fakessh")
	for _, d := range []string{home, fx.serverHome, stubs, oldStubs, sshDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitExec := strings.TrimSpace(string(execPath))
	writeScript(t, filepath.Join(stubs, "airlock"), "#!/bin/sh\necho 'airlock "+version.Version+"'\n")
	writeScript(t, filepath.Join(oldStubs, "airlock"), "#!/bin/sh\necho 'airlock 0.0.1'\n")
	fakeSSH := filepath.Join(sshDir, "ssh")
	writeScript(t, fakeSSH, fmt.Sprintf(serverShell, fx.serverHome, gitExec, stubs, oldStubs))
	t.Setenv("AIRLOCK_SSH", fakeSSH)
	return fx
}

// project makes a repository with one commit on main, under the temp dir.
func (fx *remoteFixture) project(name string) string {
	proj := filepath.Join(fx.tmp, name)
	gitT(fx.t, fx.tmp, "init", "-q", "-b", "main", proj)
	commitFile(fx.t, proj, "a.txt", "one\n")
	return proj
}

func (fx *remoteFixture) slug(proj string) string {
	fx.t.Helper()
	p, err := sandbox.Resolve(proj)
	if err != nil {
		fx.t.Fatal(err)
	}
	return p.Slug
}

// folder is where a project lives on the fake server.
func (fx *remoteFixture) folder(slug string) string {
	return filepath.Join(fx.serverHome, "airlock-remote", slug)
}

// run is airlock's Main with its output and exit code.
func (fx *remoteFixture) run(args ...string) (int, string) {
	var out, errb bytes.Buffer
	code := Main(args, &out, &errb)
	return code, out.String() + errb.String()
}

func (fx *remoteFixture) mustRun(args ...string) string {
	fx.t.Helper()
	code, msg := fx.run(args...)
	if code != 0 {
		fx.t.Fatalf("airlock %v: exit %d:\n%s", args, code, msg)
	}
	return msg
}

func fileOn(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRemoteSyncEndToEnd runs push, the refusals and pull through Main, with git
// talking to a fake server folder over the fake ssh.
func TestRemoteSyncEndToEnd(t *testing.T) {
	fx := newRemoteFixture(t)
	proj := fx.project("project")
	slug := fx.slug(proj)
	folder := fx.folder(slug)
	name := remoteName("box", slug)

	fx.mustRun("remote", "push", "box", proj)
	if got := fileOn(t, folder, "a.txt"); got != "one\n" {
		t.Errorf("server a.txt = %q after the first push", got)
	}
	if head := gitT(t, folder, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Errorf("server checkout is on %q, want main", head)
	}
	if url := gitT(t, proj, "remote", "get-url", name); url != remoteURL("box", slug) {
		t.Errorf("remote URL = %q", url)
	}

	// The second push updates the files on the server (updateInstead).
	commitFile(t, proj, "a.txt", "two\n")
	fx.mustRun("remote", "push", "box", proj)
	if got := fileOn(t, folder, "a.txt"); got != "two\n" {
		t.Errorf("server a.txt = %q after the second push", got)
	}

	// Uncommitted changes are refused; --force pushes the committed state with a warning.
	if err := os.WriteFile(filepath.Join(proj, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, msg := fx.run("remote", "push", "box", proj); code != 1 || !strings.Contains(msg, "commit or stash first; only commits travel") {
		t.Errorf("dirty push: exit %d, want 1 with the refusal:\n%s", code, msg)
	}
	if code, msg := fx.run("remote", "push", "box", proj, "--force"); code != 0 || !strings.Contains(msg, "uncommitted changes stay on this machine") {
		t.Errorf("forced push: exit %d:\n%s", code, msg)
	}
	gitT(t, proj, "checkout", "--", "a.txt")

	// The host is sanitised for the remote name, and the URL keeps the host as ssh takes it.
	commitFile(t, proj, "a.txt", "three\n")
	fx.mustRun("remote", "push", "me@box", proj)
	if got := fileOn(t, folder, "a.txt"); got != "three\n" {
		t.Errorf("server a.txt = %q, want three", got)
	}
	if url := gitT(t, proj, "remote", "get-url", remoteName("me@box", slug)); url != "me@box:airlock-remote/"+slug {
		t.Errorf("remote URL for me@box = %q", url)
	}

	// A host without airlock is refused before anything is sent.
	if code, msg := fx.run("remote", "push", "nostub", proj); code != 1 || !strings.Contains(msg, "airlock is not installed on nostub") {
		t.Errorf("push to a host without airlock: exit %d:\n%s", code, msg)
	}

	// The server works on its own branch and on main. Pull fetches both and
	// merges nothing.
	headBefore := gitT(t, proj, "rev-parse", "HEAD")
	gitT(t, folder, "checkout", "-qb", "agent-x")
	commitFile(t, folder, "agent.txt", "from the agent\n")
	gitT(t, folder, "checkout", "-q", "main")
	commitFile(t, folder, "server.txt", "on main\n")
	serverMain := gitT(t, folder, "rev-parse", "HEAD")

	msg := fx.mustRun("remote", "pull", "box", proj)
	for _, want := range []string{
		"agent-x: new branch at",
		"main: ",
		"git log --oneline " + name + "/agent-x",
		"git log --oneline main.." + name + "/main",
		"git merge " + name + "/agent-x",
		"git merge " + name + "/main",
		"nothing was merged",
		serverMain[:7],
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("pull output lacks %q:\n%s", want, msg)
		}
	}
	if gitT(t, proj, "rev-parse", "HEAD") != headBefore {
		t.Error("pull changed the local checkout")
	}
	if _, err := os.Stat(filepath.Join(proj, "agent.txt")); err == nil {
		t.Error("pull merged the server's branch into the local checkout")
	}
	if got := gitT(t, proj, "rev-parse", "refs/remotes/"+name+"/agent-x"); got == "" {
		t.Error("agent-x was not fetched")
	}

	// Nothing moved since, so the second pull says so.
	if msg := fx.mustRun("remote", "pull", "box", proj); !strings.Contains(msg, "no branch moved on box") {
		t.Errorf("second pull:\n%s", msg)
	}
}

func TestRemotePushRefusals(t *testing.T) {
	fx := newRemoteFixture(t)
	proj := fx.project("project")
	folder := fx.folder(fx.slug(proj))

	gitT(t, proj, "checkout", "-q", "--detach")
	if code, msg := fx.run("remote", "push", "box", proj); code != 1 || !strings.Contains(msg, "detached HEAD") {
		t.Errorf("detached HEAD: exit %d:\n%s", code, msg)
	}
	gitT(t, proj, "checkout", "-q", "main")

	if code, msg := fx.run("remote", "push", "box", proj, "-b", "nonexistent"); code != 1 || !strings.Contains(msg, "no local branch") {
		t.Errorf("-b nonexistent: exit %d:\n%s", code, msg)
	}
	if code, msg := fx.run("remote", "push", "box", proj, "-b", "+foo"); code != 1 || !strings.Contains(msg, "cannot be pushed") {
		t.Errorf("-b +foo: exit %d:\n%s", code, msg)
	}
	gitT(t, proj, "branch", "feat")
	fx.mustRun("remote", "push", "box", proj, "-b", "feat")
	if head := gitT(t, folder, "rev-parse", "--abbrev-ref", "HEAD"); head != "feat" {
		t.Errorf("server checkout is on %q after pushing feat", head)
	}
	fx.mustRun("remote", "push", "box", proj)

	if code, msg := fx.run("remote", "push", "unreachable", proj); code != 1 || !strings.Contains(msg, "cannot reach unreachable over ssh") {
		t.Errorf("ssh exit 255: exit %d:\n%s", code, msg)
	}
	if code, msg := fx.run("remote", "push", "box/x", proj); code != 2 || !strings.Contains(msg, "invalid remote host") {
		t.Errorf("box/x: exit %d:\n%s", code, msg)
	}
	if code, msg := fx.run("remote", "push", "oldstub", proj); code != 0 || !strings.Contains(msg, "airlock on oldstub is 0.0.1, this one is "+version.Version) {
		t.Errorf("version mismatch: exit %d, want a warning and a push:\n%s", code, msg)
	}

	// A server checkout with uncommitted changes is refused before anything is pushed.
	before := gitT(t, folder, "rev-parse", "HEAD")
	commitFile(t, proj, "a.txt", "four\n")
	if err := os.WriteFile(filepath.Join(folder, "a.txt"), []byte("server edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, msg := fx.run("remote", "push", "box", proj); code != 1 || !strings.Contains(msg, "has uncommitted changes") {
		t.Errorf("dirty server: exit %d:\n%s", code, msg)
	}
	if got := gitT(t, folder, "rev-parse", "HEAD"); got != before {
		t.Error("a refused push still moved the server's branch")
	}
	gitT(t, folder, "checkout", "--", "a.txt")

	// Pulling a project the server does not have says how to make it so.
	other := fx.project("other")
	if code, msg := fx.run("remote", "pull", "box", other); code != 1 || !strings.Contains(msg, "airlock remote push box") {
		t.Errorf("pull with no server folder: exit %d:\n%s", code, msg)
	}
}

// TestRemoteServerHomeIsARepo: a server home that is itself a git repository
// (a dotfiles repo, say) must not be touched; the project gets a repository of
// its own.
func TestRemoteServerHomeIsARepo(t *testing.T) {
	fx := newRemoteFixture(t)
	gitT(t, fx.serverHome, "init", "-q", "-b", "dotfiles")
	configBefore := fileOn(t, filepath.Join(fx.serverHome, ".git"), "config")

	proj := fx.project("project")
	fx.mustRun("remote", "push", "box", proj)

	if got := fileOn(t, filepath.Join(fx.serverHome, ".git"), "config"); got != configBefore {
		t.Error("the server's home repository config was changed")
	}
	folder := fx.folder(fx.slug(proj))
	if st, err := os.Stat(filepath.Join(folder, ".git")); err != nil || !st.IsDir() {
		t.Errorf("the project folder has no repository of its own: %v", err)
	}
	if got := fileOn(t, folder, "a.txt"); got != "one\n" {
		t.Errorf("server a.txt = %q", got)
	}
}

// TestRemotePullQuotesHostileBranchNames: a branch name chosen on the server is
// printed in hints that may be pasted, so it must come out quoted.
func TestRemotePullQuotesHostileBranchNames(t *testing.T) {
	fx := newRemoteFixture(t)
	proj := fx.project("project")
	slug := fx.slug(proj)
	fx.mustRun("remote", "push", "box", proj)

	hostile := "x;$(touch${IFS}PWNED)`id`"
	gitT(t, fx.folder(slug), "branch", hostile)
	msg := fx.mustRun("remote", "pull", "box", proj)
	ref := remoteName("box", slug) + "/" + hostile
	if want := "git merge " + shellQuote(ref); !strings.Contains(msg, want) {
		t.Errorf("merge hint is not quoted, want %q:\n%s", want, msg)
	}
	if want := "git log --oneline " + shellQuote(ref); !strings.Contains(msg, want) {
		t.Errorf("look hint is not quoted, want %q:\n%s", want, msg)
	}
}

// TestRemoteWorktreeHasItsOwnRemote: a worktree and the project root have
// folders of their own on the server and remotes of their own here.
func TestRemoteWorktreeHasItsOwnRemote(t *testing.T) {
	fx := newRemoteFixture(t)
	proj := fx.project("project")
	wt := filepath.Join(fx.tmp, "wt")
	gitT(t, proj, "worktree", "add", "-q", "-b", "feat", wt)

	fx.mustRun("remote", "push", "box", wt)
	fx.mustRun("remote", "push", "box", proj)

	rootSlug, wtSlug := fx.slug(proj), fx.slug(wt)
	if rootSlug == wtSlug {
		t.Fatal("the worktree and the root share a slug")
	}
	if rootName, wtName := remoteName("box", rootSlug), remoteName("box", wtSlug); rootName == wtName {
		t.Fatal("the worktree and the root share a remote name")
	}
	for _, slug := range []string{rootSlug, wtSlug} {
		if got := fileOn(t, fx.folder(slug), "a.txt"); got != "one\n" {
			t.Errorf("server folder %s: a.txt = %q", slug, got)
		}
	}
	if url := gitT(t, proj, "remote", "get-url", remoteName("box", wtSlug)); url != remoteURL("box", wtSlug) {
		t.Errorf("worktree remote URL = %q", url)
	}
	if url := gitT(t, proj, "remote", "get-url", remoteName("box", rootSlug)); url != remoteURL("box", rootSlug) {
		t.Errorf("root remote URL = %q", url)
	}
}

// TestRemoteEnsureScript runs the folder preparation for real, twice, in a temp
// directory: it must create a repository with updateInstead, and a second run
// must change nothing.
func TestRemoteEnsureScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	isolateGit(t)
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", "-c", ensureDirScript("airlock-remote/p-1"))
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
	}
	folder := filepath.Join(dir, "airlock-remote", "p-1")
	if got := gitT(t, folder, "config", "receive.denyCurrentBranch"); got != "updateInstead" {
		t.Errorf("receive.denyCurrentBranch = %q", got)
	}
	if got := gitT(t, folder, "rev-parse", "--show-toplevel"); got != folder {
		t.Errorf("the folder is not its own repository: top level %q", got)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// gitT runs git in dir and returns its trimmed output; the caller isolates git first.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", file)
	gitT(t, dir, "commit", "-qm", "edit "+file)
}
