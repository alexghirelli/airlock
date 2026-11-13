// Package hostgittest holds the fixtures the tests use to show that host-side
// git does not run what a sandbox agent wrote into a repository. Each attack
// has a positive control: Naive runs plain git in the same setup and must set
// the marker, so an attack that does not work cannot make a test pass.
package hostgittest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoVars are the variables that point git at a repository or configuration
// the test does not own. They are removed for the test and restored after it.
var repoVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_CONFIG", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS", "GIT_PREFIX", "GIT_QUARANTINE_PATH",
}

// Env makes the test hermetic: its own HOME, no global or system git
// configuration, no inherited repository variables, and no Docker.
func Env(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("DOCKER_HOST", "unix:///nonexistent")
	for _, k := range repoVars {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
}

// Dir is a fresh directory with its symlinks resolved.
func Dir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Naive runs plain git in dir, with no pins: what the host would run without
// hostgit. It returns git's error, if any.
func Naive(t *testing.T, dir string, args ...string) error {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Logf("naive git %v: %v\n%s", args, err, out)
	}
	return err
}

// Git runs plain git in dir with a fixed identity and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Write writes a file, creating its folder.
func Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Project is a repository "proj" with one commit on main, and a linked worktree
// "feat" under .worktrees with a tracked file changed. It returns the main
// checkout and the worktree.
func Project(t *testing.T) (main, wt string) {
	t.Helper()
	Env(t)
	base := Dir(t)
	main = filepath.Join(base, "proj")
	Git(t, base, "init", "-q", "-b", "main", main)
	Write(t, filepath.Join(main, "a.txt"), "one\n")
	Git(t, main, "add", "a.txt")
	Git(t, main, "commit", "-qm", "add a")
	wt = filepath.Join(main, ".worktrees", "feat")
	Git(t, main, "worktree", "add", "-q", "--no-track", "-b", "feat", wt)
	Write(t, filepath.Join(wt, "a.txt"), "one\ntwo\n")
	return main, wt
}

// Attacker makes a repository the agent controls, whose config runs the
// programs that touch marker: a core.fsmonitor program and a filter.x.clean
// program. It returns the repository's git directory.
func Attacker(t *testing.T, dir, marker string) string {
	t.Helper()
	evil := filepath.Join(dir, "evil")
	Git(t, dir, "init", "-q", "-b", "main", evil)
	fsmon := filepath.Join(dir, "fsmon.sh")
	Write(t, fsmon, "#!/bin/sh\ntouch '"+marker+"'\nexit 0\n")
	clean := filepath.Join(dir, "clean.sh")
	Write(t, clean, "#!/bin/sh\ntouch '"+marker+"'\ncat\n")
	if err := os.Chmod(fsmon, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(clean, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, evil, "config", "core.fsmonitor", fsmon)
	Git(t, evil, "config", "filter.x.clean", clean)
	return filepath.Join(evil, ".git")
}

// Marker is a path that does not exist yet.
func Marker(t *testing.T) string {
	t.Helper()
	return filepath.Join(Dir(t), "PWNED")
}

// Ran reports whether the marker exists: an attacker program ran.
func Ran(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// Reset removes the marker between a positive control and the real check.
func Reset(marker string) {
	_ = os.Remove(marker)
}
