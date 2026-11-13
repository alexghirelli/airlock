//go:build integration

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// at runs a script in the sandbox that serves the given directory (a worktree
// or the main checkout), starting there.
func (p *project) at(dir, script string) (string, int) {
	p.t.Helper()
	ctx, cancel := ctxMinutes(5)
	defer cancel()
	out, errOut, code := p.run(ctx, nil, "exec", dir, "--", "bash", "-c", script)
	return filterNoise(out + errOut), code
}

func (p *project) mustAt(dir, script string) string {
	p.t.Helper()
	out, code := p.at(dir, script)
	if code != 0 {
		p.t.Fatalf("in %s (%d): %s\n%s", dir, code, script, out)
	}
	return out
}

func (p *project) wt(args ...string) string {
	p.t.Helper()
	ctx, cancel := ctxMinutes(5)
	defer cancel()
	out, errOut, code := p.run(ctx, nil, append([]string{"wt"}, args...)...)
	if code != 0 {
		p.t.Fatalf("airlock wt %v (%d):\n%s%s", args, code, out, errOut)
	}
	return out + errOut
}

// sandboxes lists this project's sandboxes: the project's own and those of its isolated worktrees.
func (p *project) sandboxes() []string {
	p.t.Helper()
	ctx, cancel := ctxMinutes(1)
	defer cancel()
	out, _, _ := p.run(ctx, nil, "ls", "--json")
	var list []struct{ Name, Project string }
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		p.t.Fatalf("ls --json: %v\n%s", err, out)
	}
	var names []string
	for _, e := range list {
		if e.Project == p.Dir || strings.HasPrefix(e.Project, p.Dir+"/") {
			names = append(names, e.Name)
		}
	}
	return names
}

// cleanupWorktrees removes the worktrees' sandboxes whatever the test did.
func (p *project) cleanupWorktrees(names ...string) {
	p.t.Cleanup(func() {
		ctx, cancel := ctxMinutes(3)
		defer cancel()
		for _, n := range names {
			p.run(ctx, nil, "wt", "rm", n, "--force")
		}
	})
}

// fakeGerrit adds a bare "origin" with a change at refs/changes/NN/N/1 and returns its commit.
func fakeGerrit(p *project, change string, number string) string {
	p.git("commit", "--allow-empty", "-qm", "init")
	server := filepath.Join(filepath.Dir(p.Dir), "gerrit.git")
	p.git("init", "-q", "--bare", "-b", "main", server)
	p.git("remote", "add", "origin", server)
	p.git("push", "-q", "origin", "main")
	sha := p.git("commit-tree", "-p", "HEAD", "-m", change, "HEAD^{tree}")
	n, _ := strconv.Atoi(number)
	p.git("push", "-q", "origin", sha+fmt.Sprintf(":refs/changes/%02d/%s/1", n%100, number))
	return sha
}

// By default every worktree shares the project's sandbox and shows up as a
// Herdr workspace (a "space"), with its own task for the agent.
func TestSharedWorktreesAreHerdrWorkspacesOfOneSandbox(t *testing.T) {
	p := newProject(t, "wtshared", "")
	p.git("commit", "--allow-empty", "-qm", "init")
	if err := os.WriteFile(filepath.Join(p.Dir, "wip.txt"), []byte("main work"), 0o644); err != nil {
		t.Fatal(err)
	}

	p.cleanupWorktrees("feat", "by-agent")
	p.wt("add", "feat", "only touch includes/api", "--no-attach")
	feat := filepath.Join(p.Dir, ".worktrees", "feat")
	if got := p.sandboxes(); len(got) != 1 {
		t.Fatalf("a shared worktree must not get a sandbox of its own: %v", got)
	}

	// It runs in the project's sandbox, starting in the worktree, and sees the rest of the project.
	out := p.mustAt(feat, `pwd; echo "branch=$(git branch --show-current)"; cat `+p.Dir+`/wip.txt`)
	for _, want := range []string{feat, "branch=feat", "main work"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// A Herdr workspace named after it exists, with Pi running in it.
	ws := p.mustAt(p.Dir, `herdr workspace list; herdr agent list`)
	if !strings.Contains(ws, `"label":"feat"`) || !strings.Contains(ws, `"name":"pi-feat"`) {
		t.Errorf("no Herdr workspace and agent for feat:\n%s", ws)
	}
	// Pi started there is given the worktree's task; elsewhere it is not.
	args := p.mustAt(feat, `AIRLOCK_PI_ARGS_ONLY=1 pi`)
	if !strings.Contains(args, "--append-system-prompt") || !strings.Contains(args, "focus.md") || strings.Contains(args, "review.md") {
		t.Errorf("Pi in the worktree gets:\n%s", args)
	}
	if args := p.mustAt(p.Dir, `AIRLOCK_PI_ARGS_ONLY=1 pi`); strings.Contains(args, "focus.md") {
		t.Errorf("the project's own Pi gets a worktree's focus:\n%s", args)
	}

	// The focus can be read and changed.
	if got := strings.TrimSpace(p.wt("focus", "feat")); !strings.Contains(got, "only touch includes/api") {
		t.Errorf("focus = %q", got)
	}
	p.wt("focus", "feat", "now: the tests")
	if got := p.wt("focus", "feat"); !strings.Contains(got, "now: the tests") {
		t.Errorf("focus after change = %q", got)
	}
	if st := p.git("-C", feat, "status", "--porcelain"); st != "" {
		t.Errorf("focus files show up in git status: %q", st)
	}
	if ls := p.wt(); !strings.Contains(ls, "feat") || !strings.Contains(ls, "shared") || !strings.Contains(ls, "now: the tests") {
		t.Errorf("wt ls:\n%s", ls)
	}

	// An agent can still make a worktree of its own inside the sandbox.
	out = p.mustAt(p.Dir, `git worktree add .worktrees/by-agent -b by-agent >/dev/null 2>&1 && echo agent-worktree-ok`)
	if !strings.Contains(out, "agent-worktree-ok") {
		t.Errorf("agent could not make a worktree:\n%s", out)
	}

	// Removing closes the workspace; the project's sandbox stays.
	p.wt("rm", "feat")
	out = p.mustAt(p.Dir, `herdr workspace list`)
	if strings.Contains(out, `"feat"`) {
		t.Errorf("the workspace survived wt rm:\n%s", out)
	}
	if _, err := os.Stat(feat); err == nil {
		t.Error("worktree directory still there")
	}
	if got := p.sandboxes(); len(got) != 1 {
		t.Errorf("sandboxes after rm: %v", got)
	}
}

// With worktree_isolation = "reviews", a review has a sandbox of its own and
// cannot see the feature worktree or the rest of the project; --shared and
// --isolated override the default for one worktree.
func TestIsolatedWorktreesCannotSeeEachOther(t *testing.T) {
	p := newProject(t, "wtiso", "[sandbox]\nworktree_isolation = \"reviews\"\n")
	change := fakeGerrit(p, "a colleague's change", "777")
	if err := os.WriteFile(filepath.Join(p.Dir, "wip-secret.txt"), []byte("main work"), 0o600); err != nil {
		t.Fatal(err)
	}

	p.wt("add", "feat", "--no-attach")   // shared (own work)
	p.wt("review", "777", "--no-attach") // isolated by the config
	p.wt("add", "sealed", "--isolated", "--no-attach")
	feat := filepath.Join(p.Dir, ".worktrees", "feat")
	review := filepath.Join(p.Dir, ".worktrees", ".isolated", "review-777")
	sealed := filepath.Join(p.Dir, ".worktrees", ".isolated", "sealed")
	if got := p.sandboxes(); len(got) != 3 { // the project's (with feat), the review, sealed
		t.Fatalf("want 3 sandboxes, got %v", got)
	}

	// The review is on a local branch at the patchset and carries its task.
	if b := p.git("-C", review, "branch", "--show-current"); b != "review/777" {
		t.Errorf("review branch = %q", b)
	}
	if h := p.git("-C", review, "rev-parse", "HEAD"); h != change {
		t.Errorf("review HEAD = %s, want %s", h, change)
	}
	out := p.mustAt(review, `
pwd
echo "top=$(ls -A `+p.Dir+` | tr '\n' ' ')"
test -e `+p.Dir+`/wip-secret.txt && echo MAIN-FILE-VISIBLE || echo main-checkout-invisible
test -e `+feat+` && echo FEATURE-VISIBLE || echo feature-invisible
test -e `+sealed+` && echo SEALED-VISIBLE || echo sealed-invisible
echo fix > review-fix.txt && git add review-fix.txt && git commit -qm "review fix" && echo commit-ok
(echo x > `+p.Dir+`/.git/hooks/pre-commit) 2>/dev/null && echo HOOK-WRITABLE || echo hooks-readonly
(echo '[alias]' >> `+p.Dir+`/.git/config) 2>/dev/null && echo CONFIG-WRITABLE || echo config-readonly
(echo '[core]' >> `+p.Dir+`/.git/worktrees/review-777/config.worktree) 2>/dev/null && echo WTCONFIG-WRITABLE || echo worktree-config-readonly
git config core.hooksPath /tmp/h >/dev/null 2>&1 && echo HOOKSPATH-SET || echo hookspath-blocked
`)
	for _, want := range []string{review, "top=.git .worktrees", "main-checkout-invisible", "feature-invisible", "sealed-invisible",
		"commit-ok", "hooks-readonly", "config-readonly", "worktree-config-readonly", "hookspath-blocked"} {
		if !strings.Contains(out, want) {
			t.Errorf("review sandbox: missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"MAIN-FILE-VISIBLE", "FEATURE-VISIBLE", "SEALED-VISIBLE", "HOOK-WRITABLE", "CONFIG-WRITABLE", "WTCONFIG-WRITABLE", "HOOKSPATH-SET"} {
		if strings.Contains(out, bad) {
			t.Errorf("review sandbox breach %s:\n%s", bad, out)
		}
	}
	// Pi in the review gets the review task (and no focus, none was given).
	if args := p.mustAt(review, `AIRLOCK_PI_ARGS_ONLY=1 pi`); !strings.Contains(args, "review.md") || strings.Contains(args, "focus.md") {
		t.Errorf("Pi in the review gets:\n%s", args)
	}
	if brief := p.mustAt(review, `cat "/run/airlock/tasks/$(basename "$(git rev-parse --show-toplevel)")/review.md"`); !strings.Contains(brief, "change 777, patchset 1") || !strings.Contains(brief, "a colleague's change") {
		t.Errorf("review task:\n%s", brief)
	}

	// The project's sandbox sees the shared worktree, not the isolated ones, and cannot make one there.
	out = p.mustAt(p.Dir, `
echo "worktrees=[$(ls -A .worktrees | tr '\n' ' ')]"
echo "isolated=[$(ls -A .worktrees/.isolated | tr '\n' ' ')]"
mkdir .worktrees/.isolated/x 2>/dev/null && echo MKDIR-WORKED || echo isolated-folder-readonly
git worktree prune; git worktree list | wc -l
`)
	for _, want := range []string{"worktrees=[.isolated feat ]", "isolated=[]", "isolated-folder-readonly"} {
		if !strings.Contains(out, want) {
			t.Errorf("project sandbox: missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MKDIR-WORKED") {
		t.Errorf("the project sandbox can write into the isolated folder:\n%s", out)
	}
	// ... and pruning there (the isolated worktrees look missing) did not forget them.
	if list := p.git("worktree", "list"); !strings.Contains(list, "review-777") || !strings.Contains(list, "sealed") || !strings.Contains(list, "feat") {
		t.Errorf("a worktree was pruned from the project sandbox:\n%s", list)
	}

	// The host sees what the box committed, on the right branch.
	if log := p.git("log", "--oneline", "review/777"); !strings.Contains(log, "review fix") {
		t.Errorf("review log:\n%s", log)
	}
	if ls := p.wt("ls"); !strings.Contains(ls, "review-777") || !strings.Contains(ls, "isolated") || !strings.Contains(ls, "shared") {
		t.Errorf("wt ls:\n%s", ls)
	}

	// Removing an isolated worktree removes its sandbox, refuses uncommitted work, keeps the branch.
	os.WriteFile(filepath.Join(review, "unsaved.txt"), []byte("x"), 0o644)
	ctx, cancel := ctxMinutes(2)
	defer cancel()
	if _, _, code := p.run(ctx, nil, "wt", "rm", "review-777"); code == 0 {
		t.Error("wt rm removed a worktree with uncommitted changes")
	}
	p.wt("rm", "review-777", "--force")
	p.wt("rm", "sealed")
	if got := p.sandboxes(); len(got) != 1 {
		t.Errorf("sandboxes after removing the isolated ones: %v", got)
	}
	if p.git("branch", "--list", "review/777") == "" {
		t.Error("the review branch was deleted")
	}
	p.wt("rm", "feat")
}

// With a Herdr already open in another terminal, `airlock review` only adds the
// workspace there: no second client, no change of focus. Without one it attaches.
func TestReviewIsQuietWhenHerdrIsAlreadyOpen(t *testing.T) {
	p := newProject(t, "wtquiet", "")
	fakeGerrit(p, "a change", "555")
	p.cleanupWorktrees("review-555", "later")

	first := p.attach() // the terminal the user already has open
	first.waitFor(t, "spaces", 5*time.Minute)

	// No --no-attach: it must decide by itself not to attach (a second client
	// would need a terminal, which this process does not have).
	out := p.wt("review", "555")
	if !strings.Contains(out, "already open in another terminal") {
		t.Errorf("review did not go quiet:\n%s", out)
	}
	ws := p.mustSh(`herdr workspace list`)
	if !strings.Contains(ws, `"label":"review-555"`) {
		t.Errorf("the workspace is not in the open session:\n%s", ws)
	}
	// The user's view stays where it was: the project's own workspace keeps the focus.
	focused := p.mustSh(`herdr workspace list | jq -r '.result.workspaces[] | select(.focused) | .label'`)
	if strings.TrimSpace(focused) != "wtquiet" {
		t.Errorf("focus moved to %q", focused)
	}
	// Close that terminal: now there is no client, so the next command attaches.
	first.closeTerminal() // airlock dies with its terminal, so its lease is dead
	second := p.attach("wt", "add", "later")
	second.waitFor(t, "spaces", 2*time.Minute)
	if strings.Contains(second.output(), "already open") {
		t.Errorf("went quiet although no Herdr was open:\n%.1500s", second.output())
	}
}

// A pull request is reviewed the same way as a Gerrit change: the forge is found
// out from the remote, the branch is local, and the agent gets the task.
func TestReviewAPullRequestFromTheCLI(t *testing.T) {
	p := newProject(t, "wtpr", "")
	p.git("commit", "--allow-empty", "-qm", "init")
	server := filepath.Join(filepath.Dir(p.Dir), "forge.git")
	p.git("init", "-q", "--bare", "-b", "main", server)
	p.git("remote", "add", "origin", server)
	p.git("push", "-q", "origin", "main")
	head := p.git("commit-tree", "-p", "HEAD", "-m", "Fix the widget", "HEAD^{tree}")
	p.git("push", "-q", "origin", head+":refs/pull/21/head")
	p.cleanupWorktrees("review-pr-21")

	out := p.wt("review", "21", "--no-attach")
	if !strings.Contains(out, "pull request #21") {
		t.Errorf("output does not say what was fetched:\n%s", out)
	}
	review := filepath.Join(p.Dir, ".worktrees", "review-pr-21")
	if b := p.git("-C", review, "branch", "--show-current"); b != "review/pr-21" {
		t.Errorf("branch = %q", b)
	}
	if h := p.git("-C", review, "rev-parse", "HEAD"); h != head {
		t.Errorf("HEAD = %s, want %s", h, head)
	}
	brief := p.mustAt(review, `cat "/run/airlock/tasks/$(basename "$(git rev-parse --show-toplevel)")/review.md"`)
	for _, want := range []string{"pull request #21", "Fix the widget", head[:12], "GitHub"} {
		if !strings.Contains(brief, want) {
			t.Errorf("task lacks %q:\n%s", want, brief)
		}
	}
	// Told once more, with a number that is also a different kind, it asks.
	p.git("push", "-q", "origin", head+":refs/merge-requests/21/head")
	ctx, cancel := ctxMinutes(2)
	defer cancel()
	if _, errOut, code := p.run(ctx, nil, "review", "21", "--no-attach"); code == 0 || !strings.Contains(errOut, "pr:21") {
		t.Errorf("an ambiguous number was not refused with a hint (exit %d):\n%s", code, errOut)
	}
}
