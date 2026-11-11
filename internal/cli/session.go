package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/termio"
)

func (e *env) cmdStart(ctx context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	created, err := m.Up(ctx)
	if err != nil {
		return 1, err
	}
	e.auditAtStart(m)
	cfg := &l.res.Config
	if cfg.Team.Enabled && cfg.Sandbox.UI != "herdr" {
		return 2, fmt.Errorf("the agent team runs in Herdr panes; use --ui herdr (current UI: %s)", cfg.Sandbox.UI)
	}
	if cfg.Sandbox.UI == "herdr" {
		if err := m.PrepareHerdr(ctx, cfg.Team.Enabled); err != nil {
			return 1, err
		}
	}
	release := func() {}
	if cfg.Sandbox.UI == "herdr" {
		release = sandbox.AcquireAttach(m.Name()) // lets `airlock review` in another terminal stay quiet
	}
	code, err := m.Attach(ctx, sandbox.Command(cfg.Sandbox.UI))
	release()
	if err != nil {
		return 1, err
	}
	e.afterSession(ctx, m, created)
	return code, nil
}

// afterSession runs when an interactive session ends.
func (e *env) afterSession(ctx context.Context, m *sandbox.Manager, created bool) {
	e.auditSummary(ctx, m)
	cfg := &m.Config.Config
	if cfg.Sandbox.Lifetime == "ephemeral" && created {
		// Use a fresh context: ctx may already be cancelled by Ctrl-C.
		if err := m.Remove(context.WithoutCancel(ctx)); err != nil {
			e.errf("airlock: could not remove the ephemeral sandbox: %v", err)
		}
		return
	}
	e.errf("airlock: left %s running (airlock stop to stop it, airlock to come back)", m.Name())
}

func (e *env) cmdShell(ctx context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	created, err := m.Up(ctx)
	if err != nil {
		return 1, err
	}
	code, err := m.Attach(ctx, sandbox.Command("shell"))
	if err != nil {
		return 1, err
	}
	e.afterSession(ctx, m, created)
	return code, nil
}

func (e *env) cmdExec(ctx context.Context) (int, error) {
	if len(e.a.rest) == 0 {
		return 2, errors.New("usage: airlock exec [path] -- command [args...]")
	}
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	created, err := m.Up(ctx)
	if err != nil {
		return 1, err
	}
	defer func() {
		if l.res.Config.Sandbox.Lifetime == "ephemeral" && created {
			_ = m.Remove(context.WithoutCancel(ctx))
		}
	}()
	if termio.IsTerminal() {
		return m.Attach(ctx, e.a.rest)
	}
	var stdin = os.Stdin
	code, err := m.Exec(ctx, e.a.rest, stdinIfPiped(stdin), e.stdout, e.stderr)
	if err != nil {
		return 1, err
	}
	return code, nil
}

// stdinIfPiped returns stdin unless it is the terminal (a command that does not
// read input should not wait on the keyboard).
func stdinIfPiped(f *os.File) *os.File {
	if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	return f
}

func (e *env) cmdStop(ctx context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	info, err := m.B.Inspect(ctx, m.Name())
	if errors.Is(err, backend.ErrNotFound) {
		e.errf("airlock: no sandbox for %s", l.project.Root)
		return 0, nil
	} else if err != nil {
		return 1, err
	}
	if info.Running {
		e.auditSummary(ctx, m)
	}
	if err := m.Stop(ctx); err != nil {
		return 1, err
	}
	e.errf("airlock: stopped %s", m.Name())
	return 0, nil
}

func (e *env) cmdRm(ctx context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	if _, err := m.B.Inspect(ctx, m.Name()); errors.Is(err, backend.ErrNotFound) {
		e.errf("airlock: no sandbox for %s", l.project.Root)
		return 0, nil
	}
	if err := m.Remove(ctx); err != nil {
		return 1, err
	}
	e.errf("airlock: removed %s (your project files and login volumes are untouched)", m.Name())
	return 0, nil
}
