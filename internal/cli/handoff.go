package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/alexghirelli/airlock/internal/handoff"
	"github.com/alexghirelli/airlock/internal/inbox"
	"github.com/alexghirelli/airlock/internal/summary"
	"github.com/alexghirelli/airlock/internal/worktree"
)

// After the close, the agent list is asked again this many times, this far
// apart: Herdr removes the agent a moment after the workspace is closed.
var (
	handoffVerifyTries = 10
	handoffVerifyWait  = 200 * time.Millisecond
)

// wtHandoff hands a worktree over to a new agent (the same kind, or --agent) that
// starts with a briefing: what the worktree changed, the user's note, and where
// the previous conversation is. The previous agent must not be working (--force
// overrides). The briefing stays in the worktree's git directory until the next
// handoff or until the worktree is removed; --reset deletes it.
//
// The sandbox is brought up but no agent is started before the previous one is
// looked at: starting one only to replace it would resume its conversation for
// nothing.
func (e *env) wtHandoff(ctx context.Context, repo *worktree.Repo, l *loaded, name, note string) (int, error) {
	path, _, ok := repo.Find(name)
	if !ok {
		return 1, fmt.Errorf("no worktree %q: a handoff is for a worktree, not the project (see airlock wt ls)", name)
	}
	if e.a.reset {
		if err := worktree.ClearHandoff(repo.Root, path); err != nil {
			return 1, err
		}
		e.errf("airlock: removed the handoff briefing of %s", name)
		return 0, nil
	}
	if l.res.Config.Sandbox.UI != "herdr" {
		return 1, fmt.Errorf("a handoff needs the Herdr UI (sandbox.ui is %q)", l.res.Config.Sandbox.UI)
	}

	// The worktree's own sandbox: the project's one for a shared worktree.
	e.a.path = path
	wl, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(wl)
	if err != nil {
		return 1, err
	}
	m.NoFocus = true
	if _, err := m.Up(ctx); err != nil {
		return 1, err
	}
	b, err := newBackend(&wl.res.Config)
	if err != nil {
		return 1, err
	}

	run := handoffRun{
		name: name, path: path, note: note, kind: wl.res.Config.Agent.Kind, force: e.a.force,
		agents: func(ctx context.Context) ([]inbox.Raw, error) { return herdrAgents(ctx, b, m.Name()) },
		brief: func() (string, error) {
			s, err := summary.Collect(path, name)
			if err != nil {
				return "", err
			}
			return s.Markdown(), nil
		},
		write:   func(text string) error { return worktree.WriteHandoff(repo.Root, path, text) },
		closeWS: func(ctx context.Context) error { return e.closeWorkspace(ctx, b, l, path) },
		start: func(ctx context.Context) (int, error) {
			e.a.fresh = true // the new agent starts a new conversation, with the briefing
			return e.openWorktree(ctx, path, true)
		},
		log:   e.errf,
		tries: handoffVerifyTries,
		wait:  handoffVerifyWait,
	}
	return run.run(ctx)
}

// handoffRun is the handoff in order. Each step is a field, so the order, the
// refusal and the check after the close are tested without Herdr or Docker.
type handoffRun struct {
	name, path, note, kind string
	force                  bool

	agents  func(ctx context.Context) ([]inbox.Raw, error) // the agents of the worktree's sandbox
	brief   func() (string, error)                         // the summary Markdown of the worktree
	write   func(text string) error                        // the briefing file
	closeWS func(ctx context.Context) error                // closes the worktree's Herdr workspace
	start   func(ctx context.Context) (int, error)         // starts the new agent
	log     func(format string, v ...any)

	tries int           // how often the agents are asked after the close
	wait  time.Duration // between two of those asks
}

func (h handoffRun) run(ctx context.Context) (int, error) {
	raw, err := h.agents(ctx)
	if err != nil {
		if !h.force {
			return 1, fmt.Errorf("could not ask the sandbox for its agents (%v); pass --force to hand over anyway", err)
		}
		h.log("airlock: warning: could not ask the sandbox for its agents (%v); handing over anyway", err)
		raw = nil
	}
	prev, found := handoff.Pick(raw, h.path)
	if err := handoff.Refuse(prev, found, h.force); err != nil {
		return 1, err
	}

	md, err := h.brief()
	if err != nil {
		return 1, err
	}
	br := handoff.Briefing{Note: h.note, Summary: md}
	if found {
		p := handoff.Describe(prev)
		br.Previous = &p
	}
	// The briefing is written before the close: the new agent reads it at its start.
	if err := h.write(handoff.Render(br)); err != nil {
		return 1, err
	}
	if err := h.closeWS(ctx); err != nil {
		return 1, err
	}
	if err := h.verifyGone(ctx); err != nil {
		return 1, err
	}
	h.log("airlock: handing %s over to a new %s agent", h.name, h.kind)
	return h.start(ctx)
}

// verifyGone asks the sandbox again until no agent is left in the worktree. An
// agent that is still there would be kept by herdr-up, and the new one would
// never start fresh with the briefing, so the handoff stops instead.
func (h handoffRun) verifyGone(ctx context.Context) error {
	var last error
	for i := 0; i < h.tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(h.wait):
			}
		}
		raw, err := h.agents(ctx)
		if err != nil {
			last = err
			continue
		}
		last = nil
		if _, found := handoff.Pick(raw, h.path); !found {
			return nil
		}
	}
	if last != nil {
		return fmt.Errorf("could not confirm that the previous agent in %s is gone (%v); the briefing is written, close it in Herdr and rerun", h.name, last)
	}
	return fmt.Errorf("the previous agent is still running in %s; the briefing is written, close it in Herdr and rerun", h.name)
}
