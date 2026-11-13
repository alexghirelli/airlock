package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkRepo is a main checkout with a worktree, a user config that names
// worktree_link paths, and HOME in a temp dir. The working directory is the
// main checkout, so `airlock wt link` works on it as a user would.
func linkRepo(t *testing.T, link string) (main string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.org")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.org")
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[sandbox]\nworktree_link = "+link+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRLOCK_CONFIG", cfg)

	main = filepath.Join(home, "core")
	mustWrite(t, filepath.Join(main, "a.txt"), "one\n")
	gitIn(t, home, "init", "-q", "-b", "main", main)
	gitIn(t, main, "add", "a.txt")
	gitIn(t, main, "commit", "-qm", "init")
	mustWrite(t, filepath.Join(main, "vendor", "lib.php"), "<?php\n")
	mustWrite(t, filepath.Join(main, "LocalSettings.php"), "<?php\n")
	mustWrite(t, filepath.Join(main, "x\x1b[31m.txt"), "escape in a name\n")
	gitIn(t, main, "worktree", "add", "-q", "-b", "feat", filepath.Join(main, ".worktrees", "feat"))
	t.Chdir(main)
	return main
}

func mustWrite(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runWtLink(t *testing.T, argv ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(append([]string{"wt"}, argv...), &out, &errb)
	return code, errb.String()
}

func TestWtLinkIsIdempotentAndCreatesTheLinks(t *testing.T) {
	main := linkRepo(t, `["vendor", "LocalSettings.php"]`)
	wt := filepath.Join(main, ".worktrees", "feat")

	code, errs := runWtLink(t, "link")
	if code != 0 {
		t.Fatalf("wt link: %d\n%s", code, errs)
	}
	if got, err := os.Readlink(filepath.Join(wt, "vendor")); err != nil || got != "../../vendor" {
		t.Fatalf("vendor link: %q %v\n%s", got, err, errs)
	}
	if !strings.Contains(errs, "linked vendor -> ../../vendor") {
		t.Errorf("the link must be reported:\n%s", errs)
	}

	code, errs = runWtLink(t, "link")
	if code != 0 || !strings.Contains(errs, "0 linked") {
		t.Errorf("a second run must create nothing: %d\n%s", code, errs)
	}
	if strings.Contains(errs, "linked vendor") {
		t.Errorf("a second run must not report the link again:\n%s", errs)
	}
}

func TestWtLinkNamedWorktreeAndUnknownName(t *testing.T) {
	linkRepo(t, `["vendor"]`)
	if code, errs := runWtLink(t, "link", "feat"); code != 0 {
		t.Errorf("wt link feat: %d\n%s", code, errs)
	}
	if code, _ := runWtLink(t, "link", "nope"); code != 1 {
		t.Errorf("an unknown worktree must fail, got %d", code)
	}
}

// The names in a project are the agent's to choose: a name with an escape
// sequence must not reach the terminal as one.
func TestWtLinkEscapesHostileNames(t *testing.T) {
	linkRepo(t, `["x*"]`)
	code, errs := runWtLink(t, "link", "feat")
	if code != 0 {
		t.Fatalf("wt link: %d\n%s", code, errs)
	}
	if strings.Contains(errs, "\x1b") {
		t.Errorf("a raw escape sequence reached the output: %q", errs)
	}
	if !strings.Contains(errs, `x\x1b[31m.txt`) {
		t.Errorf("the name must be shown escaped:\n%s", errs)
	}
}

func TestWtLinkWithoutPatternsSaysSo(t *testing.T) {
	main := linkRepo(t, `[]`)
	code, errs := runWtLink(t, "link")
	if code != 0 || !strings.Contains(errs, "nothing to link") {
		t.Errorf("empty worktree_link: %d\n%s", code, errs)
	}
	if _, err := os.Lstat(filepath.Join(main, ".worktrees", "feat", "vendor")); err == nil {
		t.Error("nothing may be linked without patterns")
	}
}
