package hostgit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexghirelli/airlock/internal/hostgit"
	"github.com/alexghirelli/airlock/internal/hostgit/hostgittest"
)

// The positive control first: plain git in the same setup runs the attacker's
// program. Then hostgit must not.
func TestPlainGitRunsWhatTheAgentWroteInConfigWorktree(t *testing.T) {
	main, wt := hostgittest.Project(t)
	hostgittest.Git(t, main, "config", "extensions.worktreeConfig", "true")
	admin := filepath.Join(main, ".git", "worktrees", "feat")
	marker := hostgittest.Marker(t)
	hostgittest.Write(t, filepath.Join(admin, "config.worktree"), "[core]\n\tfsmonitor = touch "+marker+"\n")
	if err := hostgittest.Naive(t, wt, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not run the config.worktree program")
	}
}

// With extensions.worktreeConfig off, git never reads config.worktree, so the
// direct path is safe and no snapshot is taken.
func TestConfigWorktreeIgnoredWhenTheExtensionIsOff(t *testing.T) {
	main, wt := hostgittest.Project(t)
	admin := filepath.Join(main, ".git", "worktrees", "feat")
	marker := hostgittest.Marker(t)
	hostgittest.Write(t, filepath.Join(admin, "config.worktree"), "[core]\n\tfsmonitor = touch "+marker+"\n")
	loc, err := hostgit.Open(main, wt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loc.Output(context.Background(), hostgit.Options{}, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if hostgittest.Ran(marker) {
		t.Error("the config.worktree of an admin directory ran")
	}
}

// With extensions.worktreeConfig on, git would read config.worktree, in the
// worktree's admin directory or in the main checkout's .git, which the agent
// writes. hostgit runs on a snapshot without it.
func TestConfigWorktreeIgnoredWhenTheExtensionIsOn(t *testing.T) {
	for _, where := range []string{"worktree", "main"} {
		t.Run(where, func(t *testing.T) {
			main, wt := hostgittest.Project(t)
			hostgittest.Git(t, main, "config", "extensions.worktreeConfig", "true")
			dir := filepath.Join(main, ".git")
			tree := main
			if where == "worktree" {
				dir = filepath.Join(main, ".git", "worktrees", "feat")
				tree = wt
			}
			marker := hostgittest.Marker(t)
			hostgittest.Write(t, filepath.Join(dir, "config.worktree"), "[core]\n\tfsmonitor = touch "+marker+"\n")
			if err := hostgittest.Naive(t, tree, "status", "--porcelain"); err != nil {
				t.Fatal(err)
			}
			if !hostgittest.Ran(marker) {
				t.Fatal("positive control: plain git did not run config.worktree")
			}
			hostgittest.Reset(marker)
			loc, err := hostgit.Open(main, tree)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loc.Output(context.Background(), hostgit.Options{}, "status", "--porcelain"); err != nil {
				t.Fatal(err)
			}
			if hostgittest.Ran(marker) {
				t.Error("config.worktree ran under hostgit")
			}
		})
	}
}

func TestWorktreeFromItsAdminDirectoryNotItsDotGitFile(t *testing.T) {
	main, wt := hostgittest.Project(t)
	marker := hostgittest.Marker(t)
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), marker)
	hostgittest.Write(t, filepath.Join(wt, ".git"), "gitdir: "+evil+"\n")
	if err := hostgittest.Naive(t, wt, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not follow the rewritten .git file")
	}
	hostgittest.Reset(marker)
	loc, err := hostgit.Open(main, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Linked() || loc.Tree() != wt {
		t.Fatalf("worktree location: linked %v, tree %s", loc.Linked(), loc.Tree())
	}
	out, err := loc.Output(context.Background(), hostgit.Options{}, "rev-parse", "--git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), filepath.Join(".git", "worktrees", "feat")) && !strings.HasSuffix(strings.TrimSpace(out), "/feat") {
		t.Errorf("git dir is %q, not the worktree's admin directory", out)
	}
	if hostgittest.Ran(marker) {
		t.Error("the rewritten .git file's programs ran")
	}
}

func TestMainRefusesCommonDirFile(t *testing.T) {
	main, _ := hostgittest.Project(t)
	marker := hostgittest.Marker(t)
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), marker)
	hostgittest.Write(t, filepath.Join(main, ".git", "commondir"), evil+"\n")
	if err := hostgittest.Naive(t, main, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if !hostgittest.Ran(marker) {
		t.Fatal("positive control: plain git did not follow commondir")
	}
	hostgittest.Reset(marker)
	if _, err := hostgit.Main(main); err == nil || !strings.Contains(err.Error(), "commondir") {
		t.Errorf("Main accepted a checkout with a commondir file: %v", err)
	}
	if _, err := hostgit.Open(main, main); err == nil {
		t.Error("Open accepted a checkout with a commondir file")
	}
	if hostgittest.Ran(marker) {
		t.Error("the commondir target ran")
	}
}

func TestWorktreeRefusesAmbiguousAdminDirectories(t *testing.T) {
	main, wt := hostgittest.Project(t)
	// A second admin directory that names the same worktree.
	dup := filepath.Join(main, ".git", "worktrees", "dup")
	hostgittest.Write(t, filepath.Join(dup, "gitdir"), filepath.Join(wt, ".git")+"\n")
	hostgittest.Write(t, filepath.Join(dup, "commondir"), "../..\n")
	_, err := hostgit.Open(main, wt)
	if err == nil || !strings.Contains(err.Error(), "refusing to choose") {
		t.Errorf("ambiguous admin directories: %v", err)
	}
}

func TestWorktreeRefusesForeignCommondir(t *testing.T) {
	main, wt := hostgittest.Project(t)
	admin := filepath.Join(main, ".git", "worktrees", "feat")
	hostgittest.Write(t, filepath.Join(admin, "commondir"), "../../..\n")
	if _, err := hostgit.Open(main, wt); err == nil || !strings.Contains(err.Error(), "commondir") {
		t.Errorf("a commondir that is not ../..: %v", err)
	}
}

func TestWorktreesFolderMustBeARealDirectory(t *testing.T) {
	main, wt := hostgittest.Project(t)
	real := filepath.Join(main, ".git", "worktrees")
	moved := filepath.Join(main, ".git", "worktrees-real")
	if err := os.Rename(real, moved); err != nil {
		t.Fatal(err)
	}
	other := hostgittest.Dir(t)
	if err := os.Symlink(other, real); err != nil {
		t.Fatal(err)
	}
	if _, err := hostgit.Open(main, wt); err == nil {
		t.Error("a symlinked worktrees folder was accepted")
	}
}

func TestMainGitDirMustBeARealDirectory(t *testing.T) {
	main, _ := hostgittest.Project(t)
	base := hostgittest.Dir(t)
	dotgit := filepath.Join(main, ".git")
	if err := os.Rename(dotgit, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "moved"), dotgit); err != nil {
		t.Fatal(err)
	}
	if _, err := hostgit.Main(main); err == nil {
		t.Error("a symlinked .git was accepted as the main checkout")
	}
}

// A checkout inside a worktrees folder is never a root: the agent writes there.
func TestRootInsideAWorktreesFolderIsRefused(t *testing.T) {
	_, wt := hostgittest.Project(t)
	hostgittest.Write(t, filepath.Join(wt, "sub", ".git", "HEAD"), "ref: refs/heads/main\n")
	if _, err := hostgit.Main(wt); err == nil || !strings.Contains(err.Error(), "worktrees folder") {
		t.Errorf("a checkout inside .worktrees was accepted as a root: %v", err)
	}
}

func TestMainOfWorktree(t *testing.T) {
	main, wt := hostgittest.Project(t)
	if got := hostgit.MainOfWorktree(wt); got != main {
		t.Errorf("MainOfWorktree(%s) = %q, want %q", wt, got, main)
	}

	// The worktree's own .git file, rewritten by the agent, is not what decides.
	evil := hostgittest.Attacker(t, hostgittest.Dir(t), hostgittest.Marker(t))
	hostgittest.Write(t, filepath.Join(wt, ".git"), "gitdir: "+evil+"\n")
	if got := hostgit.MainOfWorktree(wt); got != main {
		t.Errorf("a rewritten .git file moved the main checkout to %q", got)
	}

	// A worktree beside the project (not in a worktrees folder) follows its .git
	// file, once the admin directory it names points back at it.
	sib := filepath.Join(hostgittest.Dir(t), "sibling")
	admin := filepath.Join(main, ".git", "worktrees", "sib")
	hostgittest.Write(t, filepath.Join(admin, "gitdir"), filepath.Join(sib, ".git")+"\n")
	hostgittest.Write(t, filepath.Join(admin, "commondir"), "../..\n")
	hostgittest.Write(t, filepath.Join(sib, ".git"), "gitdir: "+admin+"\n")
	if got := hostgit.MainOfWorktree(sib); got != main {
		t.Errorf("a sibling worktree: MainOfWorktree = %q, want %q", got, main)
	}
	// Its admin directory does not point back: no main.
	hostgittest.Write(t, filepath.Join(admin, "gitdir"), "/elsewhere/.git\n")
	if got := hostgit.MainOfWorktree(sib); got != "" {
		t.Errorf("an unverified sibling: %q", got)
	}
}

func TestMainOfWorktreeIsEmptyForAFolderWithoutGit(t *testing.T) {
	if got := hostgit.MainOfWorktree(hostgittest.Dir(t)); got != "" {
		t.Errorf("a plain folder has a main checkout: %q", got)
	}
}

func TestEnvironmentOfTheCallerDoesNotChooseTheRepository(t *testing.T) {
	main, _ := hostgittest.Project(t)
	other := hostgittest.Dir(t)
	hostgittest.Git(t, other, "init", "-q", "-b", "main", filepath.Join(other, "elsewhere"))
	t.Setenv("GIT_DIR", filepath.Join(other, "elsewhere", ".git"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
	t.Setenv("GIT_CONFIG_VALUE_0", "40")
	t.Setenv("GIT_SSH_COMMAND", "touch /nonexistent-airlock-test")
	loc, err := hostgit.Main(main)
	if err != nil {
		t.Fatal(err)
	}
	out, err := loc.Output(context.Background(), hostgit.Options{}, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != main {
		t.Errorf("GIT_DIR of the caller chose the repository: top level %q, want %q", out, main)
	}
	out, err = loc.Output(context.Background(), hostgit.Options{}, "rev-parse", "--short", "--verify", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(out)) >= 40 {
		t.Errorf("the caller's GIT_CONFIG_* reached git: %q", out)
	}
}

func TestOutputIsCapped(t *testing.T) {
	main, _ := hostgittest.Project(t)
	loc, err := hostgit.Main(main)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Output(context.Background(), hostgit.Options{MaxOutput: 8}, "log", "--format=%H")
	if !errors.Is(err, hostgit.ErrTooLarge) {
		t.Errorf("a call over its limit: %v, want ErrTooLarge", err)
	}
	out, err := loc.Output(context.Background(), hostgit.Options{}, "log", "--format=%H")
	if err != nil || strings.TrimSpace(out) == "" {
		t.Errorf("the default limit: %q, %v", out, err)
	}
}

func TestCallsHaveATimeout(t *testing.T) {
	main, _ := hostgittest.Project(t)
	loc, err := hostgit.Main(main)
	if err != nil {
		t.Fatal(err)
	}
	// A context that is already over: the call must not run to completion.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := loc.Output(ctx, hostgit.Options{}, "status"); err == nil {
		t.Error("a call past its deadline succeeded")
	}
	if _, err := loc.Output(context.Background(), hostgit.Options{Timeout: time.Nanosecond}, "status"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("a call past its own timeout: %v", err)
	}
}

// Writes copies the index back from the snapshot, so a worktree's git sees
// what a mutating call staged.
func TestWriteBackOfTheIndexOnASnapshot(t *testing.T) {
	main, wt := hostgittest.Project(t)
	hostgittest.Git(t, main, "config", "extensions.worktreeConfig", "true")
	loc, err := hostgit.Open(main, wt)
	if err != nil {
		t.Fatal(err)
	}
	hostgittest.Write(t, filepath.Join(wt, "new.txt"), "new\n")
	if _, err := loc.Output(context.Background(), hostgit.Options{Writes: true}, "add", "new.txt"); err != nil {
		t.Fatal(err)
	}
	if st := hostgittest.Git(t, wt, "status", "--porcelain"); !strings.Contains(st, "A  new.txt") {
		t.Errorf("the staged file is not in the worktree's own index:\n%s", st)
	}
}

func TestSnapshotIsRemoved(t *testing.T) {
	main, wt := hostgittest.Project(t)
	hostgittest.Git(t, main, "config", "extensions.worktreeConfig", "true")
	loc, err := hostgit.Open(main, wt)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := loc.Output(context.Background(), hostgit.Options{}, "status"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "airlock-git-") {
			t.Errorf("a snapshot was left behind: %s", e.Name())
		}
	}
}

func TestAirlockSSHBecomesGitSSHCommand(t *testing.T) {
	main, _ := hostgittest.Project(t)
	loc, err := hostgit.Main(main)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIRLOCK_SSH", "/bin/false")
	// git push to a remote over ssh runs the program; with /bin/false it fails
	// at once and never reaches the network.
	hostgittest.Git(t, main, "remote", "add", "box", "box:/x")
	if err := loc.Stream(context.Background(), hostgit.Options{}, nil, nil, "ls-remote", "box"); err == nil {
		t.Error("ls-remote over the fake ssh succeeded")
	}
}
