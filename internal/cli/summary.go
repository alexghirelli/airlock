package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/summary"
	"github.com/alexghirelli/airlock/internal/worktree"
)

const summaryUsage = "usage: airlock summary [worktree] [--json]"

// cmdSummary says what a worktree changed: the commits past its base, the files,
// what is not committed yet, and its focus. With no name it summarises the
// checkout the current directory is in (the project, or the worktree you are in).
// The project-wide audit result is added, since a worktree shares it.
func (e *env) cmdSummary(_ context.Context) (int, error) {
	if len(e.a.rest) > 1 {
		return 2, errors.New(summaryUsage)
	}
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	// A worktree that shares the project's sandbox settles to the project's Root
	// with the worktree in Cwd: the checkout the user is in is the worktree.
	path, name := l.project.Root, ""
	if l.project.Cwd != "" {
		path = l.project.Cwd
	}
	if len(e.a.rest) == 1 {
		name = e.a.rest[0]
		repo := &worktree.Repo{Root: l.project.RepoRoot(), Dir: l.res.Config.Sandbox.WorktreesDir}
		p, _, ok := repo.Find(name)
		if !ok {
			return 1, fmt.Errorf("no worktree %q (see airlock wt ls)", name)
		}
		path = p
	}
	s, err := summary.Collect(path, name)
	if err != nil {
		return 1, err
	}
	// Read-only: summary never records the audit baseline (airlock audit --accept does).
	m := &sandbox.Manager{Config: l.res, Project: l.project}
	changes, hasBaseline, err := auditChangesRO(m)
	if err != nil {
		e.errf("airlock: warning: audit: %v", err)
	} else {
		s.Audit = &summary.Audit{Baseline: hasBaseline, Changes: []string{}}
		for _, c := range changes {
			s.Audit.Changes = append(s.Audit.Changes, c.String())
		}
	}
	if e.a.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(s)
	}
	_, err = fmt.Fprint(e.stdout, s.Text())
	return 0, err
}
