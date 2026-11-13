package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alexghirelli/airlock/internal/audit"
	"github.com/alexghirelli/airlock/internal/sandbox"
)

func auditPath(p *sandbox.Project) string {
	return audit.BaselinePath(sandbox.StateDir(), p.Slug)
}

// auditChanges compares the project with its baseline. With no baseline yet it
// records one and reports nothing.
func auditChanges(m *sandbox.Manager) ([]audit.Change, error) {
	now, base, err := auditSnapshots(m)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, audit.Save(auditPath(m.Project), now)
	}
	return audit.Diff(base, now), nil
}

// auditChangesRO is auditChanges without the write: it never records a baseline.
// hasBaseline is false when there is none yet, and then there are no changes.
func auditChangesRO(m *sandbox.Manager) (changes []audit.Change, hasBaseline bool, err error) {
	now, base, err := auditSnapshots(m)
	if err != nil || base == nil {
		return nil, false, err
	}
	return audit.Diff(base, now), true, nil
}

// auditSnapshots reads the project now and the recorded baseline (nil if none).
func auditSnapshots(m *sandbox.Manager) (now, base *audit.Snapshot, err error) {
	now, err = audit.Take(m.Project.Root, m.Config.Config.Audit.ExtraPatterns)
	if err != nil {
		return nil, nil, err
	}
	base, err = audit.Load(auditPath(m.Project))
	return now, base, err
}

// auditAtStart runs when a session starts: it makes sure a baseline exists and
// warns about changes from an earlier session that were never reviewed.
func (e *env) auditAtStart(m *sandbox.Manager) {
	changes, err := auditChanges(m)
	if err != nil {
		e.errf("airlock: warning: audit: %v", err)
		return
	}
	if len(changes) > 0 {
		e.errf("airlock: %d unreviewed change(s) to host-executable files since the last review; run `airlock audit`", len(changes))
	}
}

// auditSummary prints a short report when a session ends or the sandbox stops.
func (e *env) auditSummary(_ context.Context, m *sandbox.Manager) {
	changes, err := auditChanges(m)
	if err != nil {
		e.errf("airlock: warning: audit: %v", err)
		return
	}
	if len(changes) == 0 {
		return
	}
	e.errf("airlock: %d change(s) to files that run on your computer (hooks, build and CI files, scripts, new executables):", len(changes))
	for i, c := range changes {
		if i == 8 {
			e.errf("  ... and %d more", len(changes)-8)
			break
		}
		e.errf("  %s", c)
	}
	e.errf("airlock: review with `airlock audit`; acknowledge with `airlock audit --accept`")
}

func (e *env) cmdAudit(_ context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m := &sandbox.Manager{Config: l.res, Project: l.project}
	if e.a.accept {
		return e.acceptAudit(m)
	}
	changes, err := auditChanges(m)
	if err != nil {
		return 1, err
	}
	if e.a.json {
		if changes == nil {
			changes = []audit.Change{}
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(changes)
	}
	if len(changes) == 0 {
		fmt.Fprintln(e.stdout, "no changes to host-executable files since the last review")
		return 0, nil
	}
	fmt.Fprintf(e.stdout, "%d change(s) since the last review of %s:\n", len(changes), l.project.Root)
	for _, c := range changes {
		fmt.Fprintf(e.stdout, "  %s\n", c)
	}
	fmt.Fprintln(e.stdout, "\nThese files are run or loaded automatically on your computer. Read them before using the project outside the sandbox.")
	fmt.Fprintln(e.stdout, "Acknowledge with: airlock audit --accept")
	return 0, nil
}

func (e *env) acceptAudit(m *sandbox.Manager) (int, error) {
	now, err := audit.Take(m.Project.Root, m.Config.Config.Audit.ExtraPatterns)
	if err != nil {
		return 1, err
	}
	if err := audit.Save(auditPath(m.Project), now); err != nil {
		return 1, err
	}
	e.errf("airlock: baseline updated; %d watched file(s) recorded", len(now.Files))
	return 0, nil
}
