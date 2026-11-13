//go:build integration

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The session wrapper runs as root with NET_ADMIN before it drops privileges. It
// must not look anything up in PATH, whose first entries the agent can write: a
// planted id or setpriv would run as root on the next airlock exec, shell or
// attach, and could take the firewall down.
func TestSessionWrapperIgnoresProgramsPlantedInPATH(t *testing.T) {
	p := newProject(t, "hijack", "")
	p.git("commit", "--allow-empty", "-qm", "init")

	// The planted programs record who ran them (user and parent process), then do
	// what the real ones do.
	p.mustSh(`mkdir -p ~/.local/bin ~/.local/share/mise/shims
for d in ~/.local/bin ~/.local/share/mise/shims; do
  for prog in id setpriv; do
    cat > $d/$prog <<'EOS'
#!/bin/sh
prog="${0##*/}"
echo "$prog ran as uid $(/usr/bin/id -u), parent: $(tr '\0' ' ' < /proc/$PPID/cmdline | cut -c1-160)" >> /home/agent/hijacked
exec /usr/bin/$prog "$@"
EOS
    chmod +x $d/$prog
  done
done`)

	// Which image this sandbox runs, for the log when this test fails.
	if out, err := exec.Command("docker", "inspect", "-f", "{{.Config.Image}} {{.Image}}", p.containerName()).CombinedOutput(); err == nil {
		t.Logf("sandbox image: %s", strings.TrimSpace(string(out)))
	}

	// (The log is in the home folder: /tmp is a tmpfs and does not survive a restart.)
	// Fresh sessions go through airlock-session as root. A restart runs the
	// entrypoint again, as root with NET_ADMIN and before the firewall is back: it
	// must not look anything up in PATH either (the shims folder is on a volume
	// shared by all projects).
	ctx, cancel := ctxMinutes(3)
	defer cancel()
	for i := 0; i < 2; i++ {
		p.mustSh(`true`)
		if _, errOut, code := p.run(ctx, nil, "stop"); code != 0 {
			t.Fatalf("stop: %s", errOut)
		}
	}
	p.mustSh(`true`)
	if out, _ := p.sh(`cat /home/agent/hijacked 2>/dev/null; true`); strings.TrimSpace(out) != "" {
		t.Errorf("a program planted in the agent's PATH ran as root in the session wrapper or the entrypoint:\n%s", out)
	}
	// And the session still drops its privileges.
	if out := p.mustSh(`/usr/bin/id -u; grep CapBnd /proc/self/status`); strings.Contains(out, "uid=0") || !strings.Contains(out, "CapBnd:") {
		t.Errorf("session state:\n%s", out)
	}
}

// A worktree's task files (focus, review, handoff) belong to the host: the sandbox
// reads them through a read-only mount, and nothing the agent does to the git
// directory, where they used to live, reaches the host or changes the task.
func TestTaskFilesAreHostOwnedAndReadOnlyInTheSandbox(t *testing.T) {
	p := newProject(t, "tasks", "")
	p.git("commit", "--allow-empty", "-qm", "init")
	p.cleanupWorktrees("feat")
	p.wt("add", "feat", "only touch includes/api", "--no-attach")
	feat := filepath.Join(p.Dir, ".worktrees", "feat")

	// The sandbox sees the focus, read-only, even for root.
	if out := p.mustAt(feat, `cat /run/airlock/tasks/feat/focus.md`); !strings.Contains(out, "only touch includes/api") {
		t.Errorf("the sandbox does not see the focus:\n%s", out)
	}
	for _, write := range []string{
		`echo pwned >> /run/airlock/tasks/feat/focus.md`,
		`echo pwned > /run/airlock/tasks/feat/handoff.md`,
		`rm -f /run/airlock/tasks/feat/focus.md`,
		`sudo sh -c 'echo pwned >> /run/airlock/tasks/feat/focus.md'`,
		`sudo mount -o remount,rw /run/airlock/tasks`,
	} {
		if out, code := p.at(feat, write); code == 0 {
			t.Errorf("the task folder is writable from the sandbox: %s\n%s", write, out)
		}
	}
	if args := p.mustAt(feat, `AIRLOCK_PI_ARGS_ONLY=1 pi`); !strings.Contains(args, "focus.md") {
		t.Errorf("Pi does not get the task from the mount:\n%s", args)
	}
	// Another worktree's wrapper sees no task of feat's, and the project folder none at all.
	if args := p.mustAt(p.Dir, `AIRLOCK_PI_ARGS_ONLY=1 pi`); strings.Contains(args, "focus.md") {
		t.Errorf("the project's own Pi gets a worktree's focus:\n%s", args)
	}

	// The old attacks: a link or a FIFO in the git admin directory, which the agent
	// owns. The host must neither read through it nor write through it.
	secret := filepath.Join(filepath.Dir(p.Dir), "host-secret.txt")
	victim := filepath.Join(filepath.Dir(p.Dir), "host-victim.txt")
	for path, body := range map[string]string{secret: "HOST SECRET\n", victim: "keep me\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	admin := filepath.Join(p.Dir, ".git", "worktrees", "feat")
	for _, name := range []string{"airlock-focus.md", "airlock-review.md", "airlock-handoff.md"} {
		_ = os.Remove(filepath.Join(admin, name))
	}
	if err := os.Symlink(secret, filepath.Join(admin, "airlock-focus.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(admin, "airlock-review.md")); err != nil {
		t.Fatal(err)
	}
	if got := p.wt("focus", "feat"); strings.Contains(got, "HOST SECRET") {
		t.Errorf("the host read a file through a link in the git directory:\n%s", got)
	}
	if ls := p.wt("ls"); strings.Contains(ls, "HOST SECRET") {
		t.Errorf("wt ls printed a file read through a link:\n%s", ls)
	}
	p.wt("focus", "feat", "set again")
	if b, _ := os.ReadFile(victim); string(b) != "keep me\n" {
		t.Errorf("the host wrote through a link in the git directory: %q", b)
	}
	if got := p.wt("focus", "feat"); !strings.Contains(got, "set again") {
		t.Errorf("the focus did not change: %q", got)
	}
	// What the agent sees now is the host's text, not what is in the git directory.
	if out := p.mustAt(feat, `cat /run/airlock/tasks/feat/focus.md`); !strings.Contains(out, "set again") || strings.Contains(out, "HOST SECRET") {
		t.Errorf("the sandbox reads:\n%s", out)
	}
}

// The host runs git on a repository the sandbox can write to. A worktree whose
// .git file the agent rewrote, or a main checkout with a .git/commondir, must not
// make airlock run the agent's program on the host.
func TestHostGitDoesNotRunTheAgentsPrograms(t *testing.T) {
	p := newProject(t, "hostgit", "")
	p.git("commit", "--allow-empty", "-qm", "init")
	p.cleanupWorktrees("feat")
	p.wt("add", "feat", "--no-attach")
	feat := filepath.Join(p.Dir, ".worktrees", "feat")

	marker := filepath.Join(filepath.Dir(p.Dir), "pwned")
	evil := filepath.Join(filepath.Dir(p.Dir), "evil-gitdir")
	for _, d := range []string{filepath.Join(evil, "objects"), filepath.Join(evil, "refs"), filepath.Join(evil, "info")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(evil, "HEAD"):               "ref: refs/heads/feat\n",
		filepath.Join(evil, "config"):             "[core]\n\trepositoryformatversion = 0\n\tfsmonitor = touch " + marker + "\n[filter \"x\"]\n\tclean = touch " + marker + "\n",
		filepath.Join(evil, "info", "attributes"): "* filter=x\n",
		filepath.Join(feat, ".git"):               "gitdir: " + evil + "\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(feat, "a.txt"), []byte("change\n"), 0o644)

	for _, args := range [][]string{{"wt", "ls"}, {"summary", "feat"}, {"wt", "focus", "feat"}} {
		ctx, cancel := ctxMinutes(2)
		p.run(ctx, nil, args...) // an error is fine: running the program is not
		cancel()
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("airlock %v ran a program from the rewritten .git file", args)
		}
	}
}
