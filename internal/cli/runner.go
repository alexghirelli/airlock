package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/termio"
	"github.com/alexghirelli/airlock/internal/worktree"
)

// runnerTerminal says whether the user's command gets a terminal (docker's -t,
// and no -T for compose). It is a variable so the tests do not depend on the
// terminal they run in.
var runnerTerminal = termio.IsTerminal

// runnerStopDelay is how long docker gets, after the stop signal, before it is
// killed.
const runnerStopDelay = 5 * time.Second

// runUsage is the form `airlock wt run` takes.
const runUsage = "usage: airlock wt run <runner> <worktree|.> [--print] -- <command> [args...]"

// cmdWtRun runs a command in a container the user already has, against one of the
// project's worktrees. The command is argv, never a shell line: it goes to docker
// as separate arguments, after the service or container name. Its output is not
// filtered.
func (e *env) cmdWtRun(ctx context.Context, args []string) (int, error) {
	if !e.a.dashed || len(e.a.afterDash) == 0 || len(args) < len(e.a.afterDash)+2 {
		return 2, errors.New(runUsage)
	}
	pos := args[:len(args)-len(e.a.afterDash)]
	if len(pos) != 2 {
		return 2, errors.New(runUsage)
	}
	runnerName, wtName := pos[0], pos[1]

	l, err := e.load()
	if err != nil {
		return 2, err
	}
	cfg := &l.res.Config
	r, ok := findRunner(cfg.Runners, runnerName)
	if !ok {
		return 1, unknownRunner(runnerName, cfg.Runners)
	}
	files, err := r.ComposeFiles()
	if err != nil {
		return 1, fmt.Errorf("runner %s: %w", r.Name, err)
	}

	root := l.project.RepoRoot()
	dir := root
	if wtName != "." {
		if cfg.Sandbox.WorktreesDir == "" {
			return 1, errors.New("worktrees are disabled (sandbox.worktrees_dir is empty); use . for the main checkout")
		}
		repo := &worktree.Repo{Root: root, Dir: cfg.Sandbox.WorktreesDir}
		p, _, ok := repo.Find(wtName)
		if !ok {
			return 1, fmt.Errorf("no worktree %q (see airlock wt ls)", wtName)
		}
		dir = p
	}
	rel, err := runnerRel(root, dir)
	if err != nil {
		return 1, err
	}
	argv := runnerArgv(r, files, rel, runnerTerminal(), e.a.afterDash)

	if e.a.print {
		fmt.Fprintln(e.stdout, shellJoin(argv...))
		return 0, nil
	}
	if err := checkComposeFiles(r.Name, files); err != nil {
		return 1, err
	}
	warnInsideProject(e, root, files)
	docker, err := exec.LookPath("docker")
	if err != nil {
		return 1, errors.New("docker is not on PATH: wt run needs the docker command line (--print shows the command without running it)")
	}
	cmd := exec.CommandContext(ctx, docker, argv[1:]...)
	// On cancel docker is asked to stop, and killed if it has not after the delay.
	cmd.Cancel = func() error { return stopProcess(cmd.Process) }
	cmd.WaitDelay = runnerStopDelay
	cmd.Stdin = os.Stdin
	cmd.Stdout = e.stdout
	cmd.Stderr = e.stderr
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code, nil
		}
		return 130, nil // stopped by a signal
	}
	return 1, fmt.Errorf("run docker: %w", err)
}

func findRunner(rs []config.Runner, name string) (config.Runner, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return config.Runner{}, false
}

func unknownRunner(name string, rs []config.Runner) error {
	if len(rs) == 0 {
		return fmt.Errorf("no runner %q: none is configured (add a [[runner]] to your user config, see the README)", name)
	}
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Name
	}
	return fmt.Errorf("no runner %q (configured: %s)", name, strings.Join(names, ", "))
}

// checkComposeFiles reports the first compose file that does not exist. Only
// running checks it: --print works on a file that is not there yet.
func checkComposeFiles(runner string, files []string) error {
	for _, f := range files {
		p := config.ExpandHome(f)
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("runner %s: no compose file at %s", runner, p)
		}
	}
	return nil
}

// warnInsideProject says, on stderr, which compose files resolve to a place inside
// the project. The sandbox can write there: the agent could change the file, or the
// .env next to it, and so the compose project that exec reaches. It still runs.
func warnInsideProject(e *env, root string, files []string) {
	var inside []string
	for _, f := range files {
		p := config.ExpandHome(f)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p // the existence check has run: a link that does not resolve points nowhere
		}
		if _, err := runnerRel(root, real); err == nil {
			inside = append(inside, real)
		}
	}
	if len(inside) > 0 {
		e.errf(`airlock: warning: %s is inside the project, which the sandbox can write; prefer container = "…"`, strings.Join(inside, ", "))
	}
}

// runnerRel is the worktree's path relative to the project root, with slashes:
// the part of the container's mount that the worktree is at.
func runnerRel(root, dir string) (string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not under the project root %s", dir, root)
	}
	return filepath.ToSlash(rel), nil
}

// runnerArgv is the docker command that runs argv in runner r, in the directory
// rel of the project root (slash-separated, "." for the root itself). files are
// r's compose files, in order, for the compose form. tty says whether the command
// gets a terminal: -t for container, no -T for compose.
func runnerArgv(r config.Runner, files []string, rel string, tty bool, argv []string) []string {
	workdir := path.Join(r.Mount, rel)
	var out []string
	if r.Container != "" {
		out = []string{"docker", "exec", "-i"}
		if tty {
			out = append(out, "-t")
		}
		if r.User != "" {
			out = append(out, "--user", r.User)
		}
		out = append(out, "-w", workdir, r.Container)
	} else {
		out = []string{"docker", "compose"}
		for _, f := range files {
			out = append(out, "-f", config.ExpandHome(f))
		}
		out = append(out, "exec")
		if !tty {
			out = append(out, "-T")
		}
		if r.User != "" {
			out = append(out, "--user", r.User)
		}
		out = append(out, "-w", workdir, r.Service)
	}
	return append(out, argv...)
}
