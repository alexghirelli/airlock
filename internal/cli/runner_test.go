package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexghirelli/airlock/internal/config"
)

var (
	composeRunner = config.Runner{Name: "mw", Compose: "/srv/mw/docker-compose.yml", Service: "mediawiki", Mount: "/var/www/html/w"}
	containerRun  = config.Runner{Name: "ddev", Container: "ddev-web", Mount: "/var/www/html", User: "501:20"}
	oneFile       = []string{"/srv/mw/docker-compose.yml"}
)

func TestRunnerArgvCompose(t *testing.T) {
	got := runnerArgv(composeRunner, oneFile, ".worktrees/rate-limit", false, []string{"ls", "-l", "--color"})
	want := []string{"docker", "compose", "-f", "/srv/mw/docker-compose.yml", "exec", "-T",
		"-w", "/var/www/html/w/.worktrees/rate-limit", "mediawiki", "ls", "-l", "--color"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv =\n %q\nwant\n %q", got, want)
	}
	// With a terminal there is no -T, and a user is passed as --user before -w.
	withUser := composeRunner
	withUser.User = "www-data"
	got = runnerArgv(withUser, oneFile, ".", true, []string{"php", "maintenance/run.php"})
	want = []string{"docker", "compose", "-f", "/srv/mw/docker-compose.yml", "exec",
		"--user", "www-data", "-w", "/var/www/html/w", "mediawiki", "php", "maintenance/run.php"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tty argv =\n %q\nwant\n %q", got, want)
	}
}

// Each compose file is its own -f, in the order written.
func TestRunnerArgvComposeFileList(t *testing.T) {
	files := []string{"/srv/mw/docker-compose.yml", "/srv/mw/docker-compose.override.yml"}
	got := runnerArgv(composeRunner, files, ".", false, []string{"true"})
	want := []string{"docker", "compose", "-f", "/srv/mw/docker-compose.yml", "-f", "/srv/mw/docker-compose.override.yml",
		"exec", "-T", "-w", "/var/www/html/w", "mediawiki", "true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv =\n %q\nwant\n %q", got, want)
	}
}

func TestRunnerArgvContainer(t *testing.T) {
	got := runnerArgv(containerRun, nil, ".worktrees/rate-limit", false, []string{"composer", "test"})
	want := []string{"docker", "exec", "-i", "--user", "501:20",
		"-w", "/var/www/html/.worktrees/rate-limit", "ddev-web", "composer", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv =\n %q\nwant\n %q", got, want)
	}
	got = runnerArgv(config.Runner{Name: "plain", Container: "c1", Mount: "/app"}, nil, ".", true, []string{"sh"})
	want = []string{"docker", "exec", "-i", "-t", "-w", "/app", "c1", "sh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tty argv =\n %q\nwant\n %q", got, want)
	}
}

// The directory is one argument however it looks, and the main checkout is the mount.
func TestRunnerWorkdirMapping(t *testing.T) {
	cases := map[string]string{
		".":                       "/var/www/html/w",
		".worktrees/rate-limit":   "/var/www/html/w/.worktrees/rate-limit",
		".worktrees/.isolated/x":  "/var/www/html/w/.worktrees/.isolated/x",
		".worktrees/with space/x": "/var/www/html/w/.worktrees/with space/x",
		"..foo":                   "/var/www/html/w/..foo",
	}
	for rel, want := range cases {
		argv := runnerArgv(composeRunner, oneFile, rel, false, nil)
		i := indexOf(argv, "-w")
		if i < 0 {
			t.Fatalf("no -w in %q", argv)
		}
		if got := argv[i+1]; got != want {
			t.Errorf("rel %q: workdir %q, want %q", rel, got, want)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestRunnerRelUnderRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "home", "x", "my project")
	ok := map[string]string{
		root: ".",
		filepath.Join(root, ".worktrees", "rate-limit"):       ".worktrees/rate-limit",
		filepath.Join(root, ".worktrees", ".isolated", "iso"): ".worktrees/.isolated/iso",
		filepath.Join(root, "..foo"):                          "..foo", // a name that starts with dots is inside
	}
	for dir, want := range ok {
		got, err := runnerRel(root, dir)
		if err != nil || got != want {
			t.Errorf("runnerRel(%q) = %q, %v; want %q", dir, got, err, want)
		}
	}
	for _, dir := range []string{filepath.Dir(root), filepath.Join(root, "..", "other"), "/elsewhere"} {
		if got, err := runnerRel(root, dir); err == nil {
			t.Errorf("runnerRel(%q) = %q; want a refusal", dir, got)
		}
	}
}

func TestWtRunArgumentsAreSplitAtTheDash(t *testing.T) {
	a, err := parseArgs([]string{"wt", "run", "mw", "rate-limit", "--print", "--", "ls", "-l", "--color"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.print || !a.dashed {
		t.Errorf("flags: print=%v dashed=%v", a.print, a.dashed)
	}
	if !reflect.DeepEqual(a.afterDash, []string{"ls", "-l", "--color"}) {
		t.Errorf("afterDash = %q", a.afterDash)
	}
	if !reflect.DeepEqual(a.rest, []string{"run", "mw", "rate-limit", "ls", "-l", "--color"}) {
		t.Errorf("rest = %q", a.rest)
	}
}

// runnerRepo is a repository named with a space, with a worktree and an isolated
// one, and a fake docker first in PATH. docker records its argv, one argument per
// line, in rec; FAKE_EXIT, FAKE_OUT and FAKE_ERR make it exit, print and complain.
type runnerRepo struct {
	root, wt, compose, dir, rec string
}

func setupRunnerRepo(t *testing.T) *runnerRepo {
	t.Helper()
	summaryHome(t)
	noRunnerTerminal(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &runnerRepo{root: filepath.Join(base, "my project")}
	gitIn(t, base, "init", "-q", "-b", "main", r.root)
	if err := os.WriteFile(filepath.Join(r.root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, r.root, "add", "a.txt")
	gitIn(t, r.root, "commit", "-qm", "add a")
	r.wt = filepath.Join(r.root, ".worktrees", "rate-limit")
	gitIn(t, r.root, "worktree", "add", "-q", "--no-track", "-b", "rate-limit", r.wt)
	gitIn(t, r.root, "worktree", "add", "-q", "--no-track", "-b", "iso", filepath.Join(r.root, ".worktrees", ".isolated", "iso"))

	r.compose = filepath.Join(base, "docker-compose.yml")
	if err := os.WriteFile(r.compose, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.dir = t.TempDir()
	r.rec = filepath.Join(r.dir, "argv.txt")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > '%s'
if [ -n "$FAKE_ERR" ]; then printf '%%s' "$FAKE_ERR" >&2; fi
if [ -n "$FAKE_OUT" ]; then printf '%%s\n' "$FAKE_OUT"; fi
exit "${FAKE_EXIT:-0}"
`, r.rec)
	if err := os.WriteFile(filepath.Join(r.dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", r.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r.setConfig(t, fmt.Sprintf(`[[runner]]
name = "mw"
compose = %q
service = "mediawiki"
mount = "/var/www/html/w"

[[runner]]
name = "ddev"
container = "ddev-web"
mount = "/var/www/html"
user = "501:20"
`, r.compose))
	t.Chdir(r.root)
	return r
}

// setConfig replaces the user config the test's run reads.
func (r *runnerRepo) setConfig(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(r.dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRLOCK_CONFIG", p)
}

// recorded is the argv docker was given, or nil when it was not run.
func (r *runnerRepo) recorded(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(r.rec)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func noRunnerTerminal(t *testing.T) {
	t.Helper()
	prev := runnerTerminal
	runnerTerminal = func() bool { return false }
	t.Cleanup(func() { runnerTerminal = prev })
}

func runWt(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(append([]string{"wt", "run"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestWtRunPassesArgvThroughWithoutAShell(t *testing.T) {
	r := setupRunnerRepo(t)
	t.Setenv("FAKE_EXIT", "3")
	args := []string{"mw", "rate-limit", "--", "ls", "-l", "--color", "; touch pwned", "$(touch pwned)"}
	code, _, errb := runWt(t, args...)
	if code != 3 {
		t.Fatalf("exit %d, want the command's 3; stderr %q", code, errb)
	}
	// The fake records what it was given after "docker": one line per argument.
	want := []string{"compose", "-f", r.compose, "exec", "-T", "-w", "/var/www/html/w/.worktrees/rate-limit",
		"mediawiki", "ls", "-l", "--color", "; touch pwned", "$(touch pwned)"}
	if got := r.recorded(t); !reflect.DeepEqual(got, want) {
		t.Errorf("docker got %q, want %q", got, want)
	}
	if errb != "" {
		t.Errorf("unexpected stderr %q", errb)
	}
	for _, p := range []string{r.root, r.wt} {
		if _, err := os.Stat(filepath.Join(p, "pwned")); err == nil {
			t.Errorf("a shell ran the arguments: %s/pwned exists", p)
		}
	}
}

// A list of compose files becomes one -f each, in order.
func TestWtRunComposeFileList(t *testing.T) {
	r := setupRunnerRepo(t)
	override := filepath.Join(filepath.Dir(r.compose), "docker-compose.override.yml")
	if err := os.WriteFile(override, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.setConfig(t, fmt.Sprintf("[[runner]]\nname = \"mw\"\ncompose = [%q, %q]\nservice = \"mediawiki\"\nmount = \"/w\"\n", r.compose, override))
	if code, _, errb := runWt(t, "mw", ".", "--", "true"); code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	want := []string{"compose", "-f", r.compose, "-f", override, "exec", "-T", "-w", "/w", "mediawiki", "true"}
	if got := r.recorded(t); !reflect.DeepEqual(got, want) {
		t.Errorf("docker got %q, want %q", got, want)
	}
}

// A compose file inside the project still runs, with one warning on stderr.
func TestWtRunWarnsWhenComposeIsInsideTheProject(t *testing.T) {
	r := setupRunnerRepo(t)
	inside := filepath.Join(r.root, "docker-compose.yml")
	if err := os.WriteFile(inside, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.setConfig(t, fmt.Sprintf("[[runner]]\nname = \"mw\"\ncompose = %q\nservice = \"mediawiki\"\nmount = \"/w\"\n", inside))
	code, _, errb := runWt(t, "mw", ".", "--", "true")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	wantLine := `airlock: warning: ` + inside + ` is inside the project, which the sandbox can write; prefer container = "…"`
	if strings.Count(errb, "airlock: warning:") != 1 || !strings.Contains(errb, wantLine) {
		t.Errorf("stderr %q, want the line %q", errb, wantLine)
	}
	if r.recorded(t) == nil {
		t.Error("the command did not run")
	}
}

func TestWtRunMainCheckoutAndIsolated(t *testing.T) {
	r := setupRunnerRepo(t)
	if code, _, errb := runWt(t, "ddev", ".", "--", "composer", "test"); code != 0 {
		t.Fatalf("main checkout: exit %d: %s", code, errb)
	}
	want := []string{"exec", "-i", "--user", "501:20", "-w", "/var/www/html", "ddev-web", "composer", "test"}
	if got := r.recorded(t); !reflect.DeepEqual(got, want) {
		t.Errorf("main checkout: docker got %q, want %q", got, want)
	}
	if code, _, errb := runWt(t, "ddev", "iso", "--", "ls"); code != 0 {
		t.Fatalf("isolated: exit %d: %s", code, errb)
	}
	want = []string{"exec", "-i", "--user", "501:20", "-w", "/var/www/html/.worktrees/.isolated/iso", "ddev-web", "ls"}
	if got := r.recorded(t); !reflect.DeepEqual(got, want) {
		t.Errorf("isolated: docker got %q, want %q", got, want)
	}
}

// The output is the container's and is not filtered: escape sequences pass.
func TestWtRunOutputPassesUnfiltered(t *testing.T) {
	setupRunnerRepo(t)
	t.Setenv("FAKE_OUT", "hello \x1b[31mred\x1b[0m\r")
	code, out, _ := runWt(t, "mw", "rate-limit", "--", "true")
	if code != 0 || out != "hello \x1b[31mred\x1b[0m\r\n" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
}

// docker's own complaint is plain text on stderr and reaches the user.
func TestWtRunContainerNotRunning(t *testing.T) {
	setupRunnerRepo(t)
	t.Setenv("FAKE_EXIT", "1")
	t.Setenv("FAKE_ERR", "Error response from daemon: No such container: ddev-web\n")
	code, _, errb := runWt(t, "ddev", ".", "--", "true")
	if code != 1 || !strings.Contains(errb, "No such container: ddev-web") {
		t.Errorf("exit %d, stderr %q", code, errb)
	}
}

// Cancelling the command stops docker and returns within the stop delay.
func TestWtRunCancelStopsDockerGracefully(t *testing.T) {
	r := setupRunnerRepo(t)
	if err := os.WriteFile(filepath.Join(r.dir, "docker"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := parseArgs([]string{"wt", "run", "mw", "rate-limit", "--", "true"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel)
	var out, errb bytes.Buffer
	e := &env{stdout: &out, stderr: &errb, a: a}
	start := time.Now()
	code, err := e.cmdWt(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed >= runnerStopDelay {
		t.Errorf("took %v after the cancel; want less than the stop delay %v", elapsed, runnerStopDelay)
	}
	if code == 0 {
		t.Errorf("exit 0 after a cancel")
	}
}

func TestWtRunPrintsTheCommandWithoutRunningIt(t *testing.T) {
	r := setupRunnerRepo(t)
	code, out, errb := runWt(t, "mw", "rate-limit", "--print", "--", "ls", "-l", "--color", "; touch pwned")
	if code != 0 || errb != "" {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	want := shellJoin("docker", "compose", "-f", r.compose, "exec", "-T", "-w", "/var/www/html/w/.worktrees/rate-limit",
		"mediawiki", "ls", "-l", "--color", "; touch pwned")
	if out != want+"\n" {
		t.Errorf("printed %q, want %q", out, want+"\n")
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("--print ran docker: %q", got)
	}
}

func TestWtRunPrintWorksWithoutDocker(t *testing.T) {
	setupRunnerRepo(t)
	t.Setenv("PATH", t.TempDir())
	if code, out, _ := runWt(t, "ddev", ".", "--print", "--", "true"); code != 0 || !strings.HasPrefix(out, "docker exec -i") {
		t.Errorf("--print without docker: exit %d, %q", code, out)
	}
	code, _, errb := runWt(t, "ddev", ".", "--", "true")
	if code != 1 || !strings.Contains(errb, "docker is not on PATH") {
		t.Errorf("no docker: exit %d, stderr %q", code, errb)
	}
}

func TestWtRunErrors(t *testing.T) {
	r := setupRunnerRepo(t)
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{nil, 2, "usage: airlock wt run"},
		{[]string{"mw", "rate-limit"}, 2, "usage: airlock wt run"},
		{[]string{"mw", "--", "ls"}, 2, "usage: airlock wt run"},
		{[]string{"mw", "rate-limit", "ls", "--", "x"}, 2, "usage: airlock wt run"},
		{[]string{"mw", "rate-limit", "--"}, 2, "usage: airlock wt run"},
		{[]string{"nope", "rate-limit", "--", "ls"}, 1, `no runner "nope" (configured: mw, ddev)`},
		{[]string{"mw", "nope", "--", "ls"}, 1, `no worktree "nope"`},
		{[]string{"mw", "../rate-limit", "--", "ls"}, 1, "no worktree"},
		{[]string{"mw", "", "--", "ls"}, 1, "no worktree"},
	}
	for _, c := range cases {
		code, _, errb := runWt(t, c.args...)
		if code != c.code || !strings.Contains(errb, c.msg) {
			t.Errorf("wt run %q: exit %d, stderr %q; want exit %d and %q", c.args, code, errb, c.code, c.msg)
		}
		if got := r.recorded(t); got != nil {
			t.Errorf("wt run %q ran docker: %q", c.args, got)
		}
	}
}

// A worktree name that is a link, or a folder without a .git file, is not a
// worktree: the agent can make both in the worktrees folder.
func TestWtRunRefusesLinkedWorktrees(t *testing.T) {
	r := setupRunnerRepo(t)
	dirs := filepath.Join(r.root, ".worktrees")
	if err := os.Symlink("/etc", filepath.Join(dirs, "evil")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../..", filepath.Join(dirs, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dirs, "nogit"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"evil", "up", "nogit"} {
		code, _, errb := runWt(t, "mw", name, "--", "ls")
		if code != 1 || !strings.Contains(errb, "no worktree") {
			t.Errorf("%s: exit %d, stderr %q", name, code, errb)
		}
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("docker ran for a linked worktree: %q", got)
	}
	if code, _, errb := runWt(t, "mw", "rate-limit", "--print", "--", "ls"); code != 0 {
		t.Errorf("the real worktree is refused too: %s", errb)
	}
}

func TestWtRunWithoutAnyRunner(t *testing.T) {
	r := setupRunnerRepo(t)
	r.setConfig(t, "")
	code, _, errb := runWt(t, "mw", "rate-limit", "--", "ls")
	if code != 1 || !strings.Contains(errb, "none is configured") {
		t.Errorf("exit %d, stderr %q", code, errb)
	}
}

func TestWtRunMissingComposeFile(t *testing.T) {
	r := setupRunnerRepo(t)
	missing := filepath.Join(r.dir, "gone", "docker-compose.yml")
	r.setConfig(t, fmt.Sprintf("[[runner]]\nname = \"mw\"\ncompose = %q\nservice = \"mediawiki\"\nmount = \"/w\"\n", missing))
	code, _, errb := runWt(t, "mw", "rate-limit", "--", "ls")
	if code != 1 || !strings.Contains(errb, "no compose file at "+missing) {
		t.Errorf("exit %d, stderr %q", code, errb)
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("docker ran with a missing compose file: %q", got)
	}
}

func TestWtRunUsageMentionsRun(t *testing.T) {
	code, _, errb := runWt(t)
	if code != 2 || !strings.Contains(errb, "wt run <runner>") {
		t.Errorf("exit %d, stderr %q", code, errb)
	}
}
