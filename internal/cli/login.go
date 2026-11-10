package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/termio"
)

// bridgeProvider is the Pi provider of the pi-claude-bridge package; its
// models run through Claude Code and use Claude Code's own login.
const bridgeProvider = "claude-bridge"

// loginTargets lists the Pi providers worth logging in to: the providers of
// the default model and of the team models. Providers whose credentials come
// from an environment variable in [env] need no login.
func loginTargets(c *config.Config) []string {
	seen := map[string]bool{}
	add := func(model string) {
		if prov, _, ok := strings.Cut(model, "/"); ok && prov != "" && prov != bridgeProvider {
			seen[prov] = true
		}
	}
	add(c.Pi.DefaultModel)
	for _, m := range c.Team.Models {
		add(m)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// usesBridge reports whether any configured model goes through Claude Code.
func usesBridge(c *config.Config) bool {
	if strings.HasPrefix(c.Pi.DefaultModel, bridgeProvider+"/") {
		return true
	}
	for _, m := range c.Team.Models {
		if strings.HasPrefix(m, bridgeProvider+"/") {
			return true
		}
	}
	return c.NeedsClaudeCode()
}

func (e *env) cmdLogin(ctx context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	cfg := &l.res.Config
	if e.a.reset {
		return e.resetLogins(ctx, m, cfg)
	}
	if !termio.IsTerminal() {
		return 2, errors.New("login is interactive and needs a terminal")
	}
	created, err := m.Up(ctx)
	if err != nil {
		return 1, err
	}
	defer func() {
		if cfg.Sandbox.Lifetime == "ephemeral" && created {
			_ = m.Remove(context.WithoutCancel(ctx))
		}
	}()

	targets := e.a.rest
	explicit := len(targets) > 0
	if !explicit {
		targets = loginTargets(cfg)
	}
	bridge := usesBridge(cfg) && !explicit
	if !explicit && len(targets) == 0 && !bridge {
		e.errf("airlock: no providers are configured, so there is nothing to log in to automatically.")
		e.errf("airlock: opening Pi; use its /login command, or set pi.default_model / [env] in %s", e.userConfigPath())
		return m.Attach(ctx, []string{"pi"})
	}

	for _, p := range targets {
		if !validProvider(p) {
			return 2, fmt.Errorf("invalid provider name %q", p)
		}
		e.errf("airlock: logging in to %s (credentials are stored in the %s volume, scope %q)", p, credsVolumeName(m, cfg), cfg.Credentials.Scope)
		code, err := m.Attach(ctx, []string{"pi", "auth", "login", p, "--no-browser"})
		if err != nil {
			return 1, err
		}
		if code != 0 {
			e.errf("airlock: login to %s did not complete (exit %d)", p, code)
		}
	}
	if bridge {
		e.errf("airlock: logging in to Claude Code (used by the %s models)", bridgeProvider)
		code, err := m.Attach(ctx, []string{"claude", "auth", "login"})
		if err != nil {
			return 1, err
		}
		if code != 0 {
			e.errf("airlock: Claude Code login did not complete (exit %d)", code)
		}
	}

	// Report what is ready now.
	failed := false
	for _, p := range targets {
		var out bytes.Buffer
		code, err := m.Exec(ctx, []string{"pi", "auth", "check", "--provider", p}, nil, &out, &out)
		state := strings.TrimSpace(out.String())
		if err != nil || code != 0 {
			failed = true
			e.errf("airlock: %-14s not ready (%s)", p, firstNonEmpty(state, "no credentials"))
			continue
		}
		e.errf("airlock: %-14s %s", p, firstNonEmpty(state, "ready"))
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}

func validProvider(p string) bool {
	if p == "" || len(p) > 64 {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return !strings.HasPrefix(p, "-")
}

func credsVolumeName(m *sandbox.Manager, cfg *config.Config) string {
	if cfg.Credentials.Scope == "per-project" {
		return sandbox.CredsVolumeBase + "-" + m.Project.Slug
	}
	return sandbox.CredsVolumeBase
}

// resetLogins wipes the credentials volume. It empties the volume through a
// running sandbox instead of deleting it, because a volume in use by any
// sandbox (stopped ones included) cannot be removed.
func (e *env) resetLogins(ctx context.Context, m *sandbox.Manager, cfg *config.Config) (int, error) {
	vol := credsVolumeName(m, cfg)
	scope := "every sandbox on this machine"
	if cfg.Credentials.Scope == "per-project" {
		scope = "this project's sandbox"
	}
	if !e.confirm(fmt.Sprintf("This deletes all provider logins, Pi settings, sessions and installed Pi packages stored in the %s volume, for %s. Continue?", vol, scope)) {
		return 1, errors.New("aborted (nothing was deleted)")
	}
	created, err := m.Up(ctx)
	if err != nil {
		return 1, err
	}
	if cfg.Sandbox.Lifetime == "ephemeral" && created {
		defer func() { _ = m.Remove(context.WithoutCancel(ctx)) }()
	}
	var out bytes.Buffer
	code, err := m.B.Exec(ctx, m.Name(), backend.ExecOpts{
		Cmd: []string{"find", "/creds", "-mindepth", "1", "-delete"}, User: "root", Stdout: &out, Stderr: &out,
	})
	if err != nil {
		return 1, err
	}
	if code != 0 {
		return 1, fmt.Errorf("could not empty %s: %s", vol, strings.TrimSpace(out.String()))
	}
	e.errf("airlock: wiped %s. Restart the sandbox (airlock stop; airlock) to reinstall Pi packages, then run airlock login.", vol)
	return 0, nil
}
