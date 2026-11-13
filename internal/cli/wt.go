package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/worktree"
)

const wtUsage = `usage: airlock wt add <name> [focus...] [-b branch] [--from ref]
       airlock review <change|pr:N|mr:N|bb:N|url> [focus...]
       airlock wt open <name> | focus <name> [text] | handoff <name> [note...] | ls | rm <name> [--force] | prune [--force]
       (add, review: --shared or --isolated, --no-attach; handoff: --agent, --force, --reset)`

// cmdWt manages the git worktrees of the repository around the current
// directory. A worktree either shares the project's sandbox (it gets a Herdr
// workspace in the same session) or, when it should be kept apart from the
// rest, has one of its own. sandbox.worktree_isolation picks the default.
func (e *env) cmdWt(ctx context.Context) (int, error) {
	if e.a.shared && e.a.isolated {
		return 2, errors.New("--shared and --isolated exclude each other")
	}
	if e.a.attach && e.a.noAttach {
		return 2, errors.New("--attach and --no-attach exclude each other")
	}
	sub, args := "ls", []string(nil)
	if len(e.a.rest) > 0 {
		sub, args = e.a.rest[0], e.a.rest[1:]
	}
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	cfg := &l.res.Config
	repo := &worktree.Repo{Root: l.project.RepoRoot(), Dir: cfg.Sandbox.WorktreesDir, ReviewRemote: cfg.Git.ReviewRemote, GerritRemote: cfg.Git.GerritRemote}
	repo.TasksOf = tasksOf(repo.Root, cfg.Sandbox.WorktreesDir)
	if sub != "ls" && sub != "list" && repo.Dir == "" {
		return 2, errors.New("worktrees are disabled (sandbox.worktrees_dir is empty)")
	}
	if len(args) == 0 && sub != "ls" && sub != "list" && sub != "prune" {
		return 2, errors.New(wtUsage)
	}

	switch sub {
	case "add":
		name, focus := args[0], strings.TrimSpace(strings.Join(args[1:], " "))
		isolated := e.isolate(cfg, false)
		path, err := repo.Add(name, e.a.branch, e.a.from, isolated)
		if err != nil {
			return 1, err
		}
		if focus != "" {
			if err := setFocus(repo, name, isolated, focus); err != nil {
				return 1, err
			}
		}
		e.errf("airlock: worktree %s", path)
		return e.openWorktree(ctx, path, true)

	case "review":
		change, err := worktree.ParseChange(args[0])
		if err != nil {
			return 2, err
		}
		e.errf("airlock: fetching %s", strings.TrimSpace(args[0]))
		res, err := repo.Review(change, "", e.a.branch, e.isolate(cfg, true))
		if err != nil {
			return 1, err
		}
		if focus := strings.TrimSpace(strings.Join(args[1:], " ")); focus != "" {
			if err := setFocus(repo, filepath.Base(res.Path), res.Isolated, focus); err != nil {
				return 1, err
			}
		}
		verb := "updated"
		if res.Created {
			verb = "created"
		}
		e.errf("airlock: %s %s: %s on branch %s", verb, res.Path, res.Change.What(), res.Branch)
		if res.Note != "" {
			e.errf("airlock: note: %s", res.Note)
		}
		return e.openWorktree(ctx, res.Path, true)

	case "open":
		path, _, ok := repo.Find(args[0])
		if !ok {
			return 1, fmt.Errorf("no worktree %q (see airlock wt ls)", args[0])
		}
		return e.openWorktree(ctx, path, false)

	case "focus":
		_, isolated, ok := repo.Find(args[0])
		if !ok {
			return 1, fmt.Errorf("no worktree %q (see airlock wt ls)", args[0])
		}
		if len(args) == 1 {
			dir, err := repo.TasksOf(args[0], isolated)
			if err != nil {
				return 1, err
			}
			if f := worktree.Focus(dir); f != "" {
				fmt.Fprintln(e.stdout, f)
			}
			return 0, nil
		}
		if err := setFocus(repo, args[0], isolated, strings.Join(args[1:], " ")); err != nil {
			return 1, err
		}
		e.errf("airlock: focus of %s updated; Pi reads it when it starts, so restart Pi there to apply it", args[0])
		return 0, nil

	case "handoff":
		return e.wtHandoff(ctx, repo, l, args[0], strings.TrimSpace(strings.Join(args[1:], " ")))

	case "ls", "list":
		return e.wtList(ctx, repo, l)

	case "rm", "remove":
		return e.wtRemove(ctx, repo, l, args[0])

	case "prune":
		return e.wtPrune(repo)
	}
	return 2, fmt.Errorf("unknown wt command %q\n%s", sub, wtUsage)
}

// isolate says whether a new worktree gets a sandbox of its own: what the flags
// ask for, otherwise what sandbox.worktree_isolation says for this kind.
func (e *env) isolate(cfg *config.Config, review bool) bool {
	switch {
	case e.a.isolated:
		return true
	case e.a.shared:
		return false
	}
	switch cfg.Sandbox.WorktreeIsolation {
	case "all":
		return true
	case "reviews":
		return review
	}
	return false
}

// openWorktree starts (or resumes) the sandbox the worktree belongs to and
// attaches to it. With --no-attach it only makes it ready, and a worktree that
// shares the project's sandbox gets its Herdr workspace without taking the focus.
// `add` and `review` do the same on their own when a Herdr is already open in
// another terminal: the new workspace appears there, so a second client would
// only get in the way. --attach attaches anyway.
func (e *env) openWorktree(ctx context.Context, path string, quietIfOpen bool) (int, error) {
	e.a.path = path
	if !e.a.noAttach && !e.a.attach && quietIfOpen && e.herdrOpenElsewhere() {
		e.errf("airlock: Herdr is already open in another terminal: the workspace is added there")
		e.a.noAttach = true
	}
	if !e.a.noAttach {
		return e.cmdStart(ctx)
	}
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	m.NoFocus = true
	if _, err := m.Up(ctx); err != nil {
		return 1, err
	}
	if l.res.Config.Sandbox.UI == "herdr" {
		if err := m.PrepareHerdr(ctx, false); err != nil {
			return 1, err
		}
	}
	e.errf("airlock: %s is ready (airlock %s to attach)", m.Name(), path)
	return 0, nil
}

// herdrOpenElsewhere reports whether another airlock process on this machine
// has a Herdr client attached to the sandbox for e.a.path.
func (e *env) herdrOpenElsewhere() bool {
	l, err := e.load()
	return err == nil && l.res.Config.Sandbox.UI == "herdr" && sandbox.AttachedElsewhere(l.project.ContainerName())
}

type wtRow struct {
	worktree.Entry
	Mode    string `json:"mode"`    // shared | isolated
	Sandbox string `json:"sandbox"` // running | stopped | none
}

func (e *env) wtList(ctx context.Context, repo *worktree.Repo, l *loaded) (int, error) {
	list, err := repo.List()
	if err != nil {
		return 1, err
	}
	b, err := newBackend(&l.res.Config)
	if err != nil {
		return 1, err
	}
	pingErr := b.Ping(ctx)
	rows := make([]wtRow, 0, len(list))
	for _, w := range list {
		row := wtRow{Entry: w, Mode: "shared", Sandbox: "none"}
		if w.Isolated {
			row.Mode = "isolated"
		}
		// A shared worktree runs in the project's sandbox, an isolated one in its own.
		if p, err := sandbox.Resolve(w.Path); err == nil && pingErr == nil {
			if info, err := b.Inspect(ctx, p.Settle(l.res.Config.Sandbox.WorktreesDir).ContainerName()); err == nil {
				row.Sandbox = "stopped"
				if info.Running {
					row.Sandbox = "running"
				}
			}
		}
		rows = append(rows, row)
	}
	if e.a.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.stdout, "no worktrees yet; airlock wt add <name> or airlock review <change>")
		return 0, nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tBRANCH\tHEAD\tCHANGES\tSANDBOX\tFOCUS")
	for _, r := range rows {
		branch, changes := r.Branch, "clean"
		if branch == "" {
			branch = "(detached)"
		}
		if r.Dirty {
			changes = "uncommitted"
		}
		focus := ""
		if dir, err := repo.TasksOf(r.Name, r.Isolated); err == nil {
			focus = firstLine(worktree.Focus(dir), 40)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s (%s)\t%s\n", r.Name, branch, r.Head, changes, r.Sandbox, r.Mode, focus)
	}
	return 0, tw.Flush()
}

// tasksOf is the TasksOf of the worktrees of the repository at root: each
// worktree's task folder, in the sandbox that serves it.
func tasksOf(root, worktreesDir string) func(string, bool) (string, error) {
	return func(name string, isolated bool) (string, error) {
		return sandbox.WorktreeTasks(root, worktreesDir, name, isolated)
	}
}

// setFocus writes the focus of the named worktree to its task folder.
func setFocus(repo *worktree.Repo, name string, isolated bool, text string) error {
	dir, err := repo.TasksOf(name, isolated)
	if err != nil {
		return err
	}
	return worktree.SetFocus(dir, text)
}

func firstLine(s string, max int) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// wtPrune lists the worktrees whose folder was deleted by hand and, with --force,
// forgets them (the branches stay). `airlock gc` removes the sandboxes they had.
func (e *env) wtPrune(repo *worktree.Repo) (int, error) {
	stale, err := repo.Stale()
	if err != nil {
		return 1, err
	}
	if len(stale) == 0 {
		fmt.Fprintln(e.stdout, "no stale worktrees")
		return 0, nil
	}
	for _, w := range stale {
		fmt.Fprintf(e.stdout, "stale: %s  %s  %s\n", w.Name, w.Branch, w.Path)
	}
	if !e.a.force {
		e.errf("airlock: nothing was removed; run airlock wt prune --force to forget these (their branches stay)")
		return 0, nil
	}
	if err := repo.PruneStale(stale); err != nil {
		return 1, err
	}
	e.errf("airlock: forgot %d worktree(s)", len(stale))
	return 0, nil
}

func (e *env) wtRemove(ctx context.Context, repo *worktree.Repo, l *loaded, name string) (int, error) {
	path, isolated, ok := repo.Find(name)
	if !ok {
		return 1, fmt.Errorf("no worktree %q (see airlock wt ls)", name)
	}
	// Refuse before touching any sandbox, so a refusal costs nothing.
	if !e.a.force {
		list, err := repo.List()
		if err != nil {
			return 1, err
		}
		for _, w := range list {
			if w.Path == path && w.Dirty {
				return 1, fmt.Errorf("%s has uncommitted changes; commit or discard them, or use --force", name)
			}
		}
	}
	if b, err := newBackend(&l.res.Config); err == nil && b.Ping(ctx) == nil {
		if isolated {
			if p, err := sandbox.Resolve(path); err == nil {
				if err := b.Remove(ctx, p.ContainerName(), true); err != nil && !errors.Is(err, backend.ErrNotFound) {
					return 1, err
				}
			}
		} else {
			if err := e.closeWorkspace(ctx, b, l, path); err != nil {
				e.errf("airlock: warning: %v", err)
			}
		}
	}
	// Found before the removal: the folder is worked out from the worktree's path.
	tasks, taskErr := repo.TasksOf(name, isolated)
	branch, err := repo.Remove(name, e.a.force)
	if err != nil {
		return 1, err
	}
	if taskErr == nil {
		if err := worktree.RemoveTasks(tasks); err != nil {
			e.errf("airlock: warning: could not delete the task files of %s: %v", name, err)
		}
	}
	e.errf("airlock: removed worktree %s", name)
	if branch != "" {
		e.errf("airlock: branch %s is kept (git branch -D %s to delete it)", branch, branch)
	}
	return 0, nil
}

// closeWorkspace closes a shared worktree's Herdr workspace (and the agent in
// it) when the project's sandbox is running. Nothing to close is not an error.
// Callers that must know the agent is gone (handoff) check the result; removing
// a worktree only warns.
func (e *env) closeWorkspace(ctx context.Context, b backend.Backend, l *loaded, path string) error {
	if l.res.Config.Sandbox.UI != "herdr" {
		return nil
	}
	p, err := sandbox.Resolve(path)
	if err != nil {
		return err
	}
	p = p.Settle(l.res.Config.Sandbox.WorktreesDir)
	info, err := b.Inspect(ctx, p.ContainerName())
	if err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			return nil // no sandbox, so no workspace to close (wt rm of a worktree never started)
		}
		return fmt.Errorf("could not look at the sandbox of %s: %w", path, err)
	}
	if !info.Running {
		return nil
	}
	m, err := e.manager(&loaded{project: p, res: l.res})
	if err != nil {
		return err
	}
	code, err := m.Exec(ctx, []string{"airlock-herdr-up", "--close", p.WorkDir()}, nil, e.stderr, e.stderr)
	if err != nil {
		return fmt.Errorf("could not close the Herdr workspace of %s: %w", path, err)
	}
	if code != 0 {
		return fmt.Errorf("could not close the Herdr workspace of %s (exit %d)", path, code)
	}
	return nil
}
