package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkFixture is a main checkout with tracked and untracked project files, and a
// worktree of it made by Repo.Add. HOME and the git configuration are the test's
// own, so nothing reads the user's.
func linkFixture(t *testing.T) (repo *Repo, main, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_DIR", "")
	os.Unsetenv("GIT_DIR")

	main = filepath.Join(tmp, "core")
	git(t, tmp, "init", "-q", "-b", "main", main)
	commit(t, main, "a.txt", "one\n")
	writeFile(t, main, filepath.Join("extensions", "Bar", "Bar.php"), "tracked\n")
	git(t, main, "add", "extensions")
	git(t, main, "commit", "-qm", "add Bar")
	writeFile(t, main, "LocalSettings.php", "<?php // secret\n")
	writeFile(t, main, filepath.Join("vendor", "lib.php"), "<?php\n")
	writeFile(t, main, filepath.Join("extensions", "Foo", "Foo.php"), "<?php\n")
	writeFile(t, main, filepath.Join("skins", "Chameleon", "skin.php"), "<?php\n")
	writeFile(t, main, ".env", "TOKEN=x\n")

	repo = &Repo{Root: main, Dir: ".worktrees"}
	wt, err = repo.Add("feat", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	return repo, main, wt
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var linkPatterns = []string{"LocalSettings.php", "vendor", "extensions/*", "skins/*", ".env", "missing"}

func TestLinkCreatesRelativeLinksToTheMainCheckout(t *testing.T) {
	repo, main, wt := linkFixture(t)
	res, err := repo.Link(wt, false, linkPatterns)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"LocalSettings.php": "../../LocalSettings.php",
		"vendor":            "../../vendor",
		"skins/Chameleon":   "../../../skins/Chameleon",
		"extensions/Foo":    "../../../extensions/Foo",
		".env":              "../../.env",
	}
	got := map[string]string{}
	for _, l := range res.Linked {
		got[l.Path] = l.Target
	}
	for p, target := range want {
		if got[p] != target {
			t.Errorf("link %s: target %q, want %q (linked: %v)", p, got[p], target, res.Linked)
		}
		link, err := os.Readlink(filepath.Join(wt, filepath.FromSlash(p)))
		if err != nil || link != target {
			t.Errorf("%s in the worktree: readlink %q %v, want %q", p, link, err, target)
		}
	}
	if len(res.Linked) != len(want) {
		t.Errorf("linked %d paths, want %d: %v", len(res.Linked), len(want), res.Linked)
	}
	// The link works: the worktree reads the main checkout's files through it.
	if data, err := os.ReadFile(filepath.Join(wt, "vendor", "lib.php")); err != nil || string(data) != "<?php\n" {
		t.Errorf("reading through the vendor link: %q %v", data, err)
	}
	// The tracked folder is not replaced; the missing pattern and the tracked
	// match are reported, not fatal.
	if fi, err := os.Lstat(filepath.Join(wt, "extensions", "Bar")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a tracked folder must be left alone: %v %v", fi, err)
	}
	joined := strings.Join(res.Skipped, "\n")
	if !strings.Contains(joined, "missing: matches nothing") {
		t.Errorf("skipped must say the pattern matches nothing:\n%s", joined)
	}
	// Tracked paths are counted, not listed one by one.
	if res.Tracked != 1 || strings.Contains(joined, "tracked") {
		t.Errorf("tracked: %d, skipped %v", res.Tracked, res.Skipped)
	}
	// Main is untouched.
	if _, err := os.Lstat(filepath.Join(main, "vendor", "lib.php")); err != nil {
		t.Error(err)
	}
}

func TestLinkNeverOverwritesOrRemoves(t *testing.T) {
	repo, _, wt := linkFixture(t)
	writeFile(t, wt, "LocalSettings.php", "the worktree's own\n")
	res, err := repo.Link(wt, false, []string{"LocalSettings.php", "vendor"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(wt, "LocalSettings.php"))
	if err != nil || string(data) != "the worktree's own\n" {
		t.Errorf("an existing file was changed: %q %v", data, err)
	}
	if fi, err := os.Lstat(filepath.Join(wt, "LocalSettings.php")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("an existing file was replaced by a link: %v %v", fi, err)
	}
	if res.Present != 1 || len(res.Linked) != 1 {
		t.Errorf("the existing file is counted as present; vendor is linked: present %d, %+v", res.Present, res.Linked)
	}
}

func TestLinkSkipsAPathTrackedInTheWorktreesBranch(t *testing.T) {
	repo, main, wt := linkFixture(t)
	// The branch tracks shared.cfg, and the worktree no longer has it; main has an
	// untracked file of the same name. Linking would replace the branch's file.
	writeFile(t, wt, "shared.cfg", "branch\n")
	git(t, wt, "add", "shared.cfg")
	git(t, wt, "commit", "-qm", "add shared.cfg")
	if err := os.Remove(filepath.Join(wt, "shared.cfg")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, main, "shared.cfg", "main\n")
	res, err := repo.Link(wt, false, []string{"shared.cfg"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 {
		t.Errorf("a path tracked in the worktree's branch must not be linked: %v", res.Linked)
	}
	if res.Tracked != 1 {
		t.Errorf("a path tracked in the branch is counted as tracked: %+v", res)
	}
}

// The positive control: a plain os.Symlink through a folder that is a link to an
// outside folder writes outside. Then Link, in the same situation, must not.
func TestLinkDoesNotFollowAFolderThatIsALinkOutOfTheWorktree(t *testing.T) {
	repo, main, wt := linkFixture(t)
	outside := t.TempDir()
	entry := filepath.Join(main, "extensions", "Foo")

	// Setup: the worktree's extensions folder is replaced by a link to outside.
	if err := os.RemoveAll(filepath.Join(wt, "extensions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wt, "extensions")); err != nil {
		t.Fatal(err)
	}

	// Positive control: the naive way escapes the worktree.
	if err := os.Symlink(entry, filepath.Join(wt, "extensions", "Foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "Foo")); err != nil {
		t.Fatalf("control: the naive link should have been created outside, showing the escape: %v", err)
	}
	if err := os.Remove(filepath.Join(outside, "Foo")); err != nil {
		t.Fatal(err)
	}

	res, err := repo.Link(wt, false, []string{"extensions/Foo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 {
		t.Errorf("linked through a link out of the worktree: %v", res.Linked)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was created outside the worktree: %v", entries)
	}
	if !strings.Contains(strings.Join(res.Skipped, "\n"), "is a link: not followed") {
		t.Errorf("skipped: %v", res.Skipped)
	}
}

func TestLinkMakesMissingFoldersInsideTheWorktree(t *testing.T) {
	repo, _, wt := linkFixture(t)
	// extensions/ exists in git for core; here it is a folder git does not know about.
	if err := os.RemoveAll(filepath.Join(wt, "extensions")); err != nil {
		t.Fatal(err)
	}
	res, err := repo.Link(wt, false, []string{"extensions/Foo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 1 {
		t.Fatalf("linked: %v skipped: %v", res.Linked, res.Skipped)
	}
	fi, err := os.Lstat(filepath.Join(wt, "extensions"))
	if err != nil || !fi.IsDir() {
		t.Errorf("the missing folder must be made as a real folder: %v %v", fi, err)
	}
	if _, err := os.ReadFile(filepath.Join(wt, "extensions", "Foo", "Foo.php")); err != nil {
		t.Errorf("reading through the new link: %v", err)
	}
}

func TestLinkIsIdempotent(t *testing.T) {
	repo, _, wt := linkFixture(t)
	first, err := repo.Link(wt, false, linkPatterns)
	if err != nil || len(first.Linked) == 0 {
		t.Fatalf("first run: %v %v", first.Linked, err)
	}
	second, err := repo.Link(wt, false, linkPatterns)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Linked) != 0 {
		t.Errorf("second run must create nothing: %v", second.Linked)
	}
	if second.Already != len(first.Linked) || len(second.Skipped) > 1 {
		t.Errorf("the links that are there count as already linked, and nothing else is noise: %+v", second)
	}
}

func TestLinkSkipsIsolatedWorktrees(t *testing.T) {
	repo, _, wt := linkFixture(t)
	res, err := repo.Link(wt, true, linkPatterns)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "dangle") {
		t.Errorf("an isolated worktree gets no links and one note: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(wt, "vendor")); err == nil {
		t.Error("an isolated worktree must not get links")
	}
}

func TestLinkSkipsMatchesThatLeaveTheProject(t *testing.T) {
	repo, main, wt := linkFixture(t)
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "outside\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(main, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "gone"), filepath.Join(main, "dangle")); err != nil {
		t.Fatal(err)
	}
	res, err := repo.Link(wt, false, []string{"leak.txt", "dangle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 {
		t.Errorf("a link out of the project must not be followed or linked: %v", res.Linked)
	}
	joined := strings.Join(res.Skipped, "\n")
	if !strings.Contains(joined, "leak.txt: a link that leads outside the project") || !strings.Contains(joined, "dangle: does not resolve") {
		t.Errorf("skipped: %v", res.Skipped)
	}
}

func TestLinkNeverTouchesGitOrTheWorktreesFolder(t *testing.T) {
	repo, _, wt := linkFixture(t)
	res, err := repo.Link(wt, false, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range res.Linked {
		if inFolders(l.Path, []string{".git", ".worktrees"}) {
			t.Errorf("linked inside git's folder or the worktrees folder: %v", l)
		}
	}
	if _, err := os.Lstat(filepath.Join(wt, ".git")); err != nil {
		t.Errorf("the worktree's own .git must be untouched: %v", err)
	}
	if !strings.Contains(strings.Join(res.Skipped, "\n"), ".git: inside git's folder") {
		t.Errorf("skipped: %v", res.Skipped)
	}
}

// The configured worktrees folder is never linked, whatever its name: a pattern
// that matches it is skipped, as .worktrees is.
func TestLinkSkipsTheConfiguredWorktreesFolder(t *testing.T) {
	_, main, wt := linkFixture(t)
	if err := os.MkdirAll(filepath.Join(main, "wt", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := &Repo{Root: main, Dir: "wt/sub"}
	res, err := repo.Link(wt, false, []string{"wt", ".worktrees", "vendor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range res.Linked {
		if l.Path == "wt" || l.Path == ".worktrees" {
			t.Errorf("a worktrees folder was linked: %v", l)
		}
	}
	if len(res.Linked) != 1 || res.Linked[0].Path != "vendor" {
		t.Errorf("only vendor may be linked: %+v", res)
	}
}

func TestLinkRefusesAPathThatIsNotAWorktree(t *testing.T) {
	repo, main, _ := linkFixture(t)
	if _, err := repo.Link(main, false, []string{"vendor"}); err == nil {
		t.Error("the main checkout is not a worktree to link into")
	}
	if _, err := repo.Link(t.TempDir(), false, []string{"vendor"}); err == nil {
		t.Error("a folder that is not a worktree must be refused")
	}
}
