package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/worktree"
)

// linkNew applies sandbox.worktree_link to a worktree that was just created. A
// failure is a warning: the worktree exists and works without its links.
func (e *env) linkNew(repo *worktree.Repo, cfg *config.Config, path string, isolated bool) {
	if len(cfg.Sandbox.WorktreeLink) == 0 {
		return
	}
	lk, err := repo.Linker(cfg.Sandbox.WorktreeLink)
	if err == nil {
		err = e.linkWorktree(lk, filepath.Base(path), path, isolated)
	}
	if err != nil {
		e.errf("airlock: warning: worktree_link: %s", escapeLine(err.Error()))
	}
}

// linkWorktree makes the links of one worktree and says what it did: each link
// made, each path left alone that needs a look, then one count line. Links that
// are already right and paths git tracks are counted, not listed. Nothing it
// prints is raw: the names come from the project, which the agent can write.
func (e *env) linkWorktree(lk *worktree.Linker, name, path string, isolated bool) error {
	res, err := lk.Link(path, isolated)
	if err != nil {
		return err
	}
	for _, l := range res.Linked {
		e.errf("airlock: linked %s -> %s", escapeLine(l.Path), escapeLine(l.Target))
	}
	for _, s := range res.Skipped {
		e.errf("airlock: not linked: %s", escapeLine(s))
	}
	e.errf("airlock: worktree_link in %s: %d linked, %d already linked, %d already in the worktree, %d tracked by git (left alone)",
		escapeLine(name), len(res.Linked), res.Already, res.Present, res.Tracked)
	return nil
}

// wtLink is `airlock wt link [name]`: it applies sandbox.worktree_link again to
// one worktree, or to every worktree. The project's tracked files are read once
// for all of them. Links that exist are left as they are.
func (e *env) wtLink(repo *worktree.Repo, patterns []string, args []string) (int, error) {
	if len(patterns) == 0 {
		e.errf("airlock: sandbox.worktree_link is empty: nothing to link (see the README, Settings for one project)")
		return 0, nil
	}
	var list []worktree.Entry
	if len(args) > 0 {
		p, isolated, ok := repo.Find(args[0])
		if !ok {
			return 1, fmt.Errorf("no worktree %q (see airlock wt ls)", args[0])
		}
		list = []worktree.Entry{{Name: args[0], Path: p, Isolated: isolated}}
	} else {
		all, err := repo.List()
		if err != nil {
			return 1, err
		}
		list = all
	}
	if len(list) == 0 {
		e.errf("airlock: no worktrees yet; airlock wt add <name> or airlock review <change>")
		return 0, nil
	}
	lk, err := repo.Linker(patterns)
	if err != nil {
		return 1, err
	}
	code := 0
	for _, w := range list {
		if err := e.linkWorktree(lk, w.Name, w.Path, w.Isolated); err != nil {
			e.errf("airlock: warning: worktree_link in %s: %s", escapeLine(w.Name), escapeLine(err.Error()))
			code = 1
		}
	}
	return code, nil
}

// escapeLine writes s for a terminal on one line: every character that is not
// printable, a newline included, is shown with a Go escape, and invalid bytes as
// \x.. bytes.
func escapeLine(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case strconv.IsPrint(r):
			b.WriteString(s[i : i+size])
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
		i += size
	}
	return b.String()
}
