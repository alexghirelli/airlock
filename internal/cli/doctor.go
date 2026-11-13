package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/doctor"
	"github.com/alexghirelli/airlock/internal/ignore"
	"github.com/alexghirelli/airlock/internal/imagebuild"
	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/version"
)

func (e *env) cmdDoctor(ctx context.Context) (int, error) {
	r := &doctor.Report{}
	l, lerr := e.load()
	if lerr != nil {
		// A broken configuration is itself the finding; fall back to defaults
		// for the checks that do not depend on it.
		r.Add("configuration", doctor.Fail, lerr.Error(), "fix the file named above (airlock config path shows the user config)")
		res, err := e.loadUser()
		if err != nil {
			return e.finishDoctor(r)
		}
		l = &loaded{res: res}
	} else {
		detail := "user config: " + l.res.UserConfigPath
		if !l.res.UserConfigExists {
			detail += " (not created yet; using defaults, `airlock config init` writes a template)"
		}
		if l.res.ProjectConfigPath != "" {
			detail += "; project config: " + l.res.ProjectConfigPath
		}
		r.Add("configuration", doctor.OK, detail, "")
		for _, f := range l.res.ProjectFiles {
			r.Add("project settings", doctor.OK, f, "")
		}
		for _, w := range l.res.Warnings {
			r.Add("configuration warning", doctor.Warn, w, "")
		}
	}
	cfg := &l.res.Config

	b, err := newBackend(cfg)
	if err != nil {
		r.Add("backend", doctor.Fail, err.Error(), "set sandbox.backend to container or gvisor")
		return e.finishDoctor(r)
	}
	if err := b.Ping(ctx); err != nil {
		r.Add("container engine", doctor.Fail, err.Error(), "start Docker (or OrbStack, Colima, Podman's Docker-compatible socket) and check DOCKER_HOST")
		return e.finishDoctor(r)
	}
	info, err := b.Info(ctx)
	if err != nil {
		r.Add("container engine", doctor.Fail, err.Error(), "")
		return e.finishDoctor(r)
	}
	r.Add("container engine", doctor.OK, fmt.Sprintf("%s %s (%s, %s)", info.Name, info.Version, info.OS, info.Arch), "")
	if info.DockerDesktop {
		r.Add("runtime", doctor.OK, "Docker Desktop: the host is reached through a private address, which the firewall blocks", "")
	}
	for _, c := range doctor.PlatformNotes(runtime.GOOS, projectRoot(l), info.Rootless) {
		r.Checks = append(r.Checks, c)
	}
	if info.SELinux {
		r.Add("selinux", doctor.OK, "the engine enforces SELinux; bind mounts are started with label=disable so they stay readable", "")
	}

	// Backend availability.
	switch cfg.Sandbox.Backend {
	case "gvisor":
		if hasRuntime(info, "runsc") {
			r.Add("backend gvisor", doctor.OK, "the runsc runtime is installed", "")
		} else {
			r.Add("backend gvisor", doctor.Fail, "the runsc runtime is not registered with the engine",
				"install gVisor (https://gvisor.dev/docs/user_guide/install/), run `runsc install`, restart the engine. Linux only: Docker Desktop on macOS/Windows does not ship runsc")
		}
	default:
		if hasRuntime(info, "runsc") {
			r.Add("backend gvisor", doctor.OK, "available: set sandbox.backend = \"gvisor\" for a user-space kernel", "")
		} else {
			r.Add("backend gvisor", doctor.Skip, "runsc not installed (optional, Linux only)", "https://gvisor.dev/docs/user_guide/install/")
		}
	}

	// Image.
	base := imagebuild.BaseRef(cfg)
	if ok, _ := b.ImageExists(ctx, base); ok {
		r.Add("image", doctor.OK, base, "")
	} else {
		r.Add("image", doctor.Skip, base+" is not built yet", "it is built on first use; or run `airlock build`")
	}

	// Mounts.
	for _, m := range cfg.Mounts {
		if st, err := os.Stat(m.Host); err != nil {
			r.Add("mount "+m.Name, doctor.Warn, m.Host+" does not exist on this machine; it will be skipped", "")
		} else if !st.IsDir() {
			r.Add("mount "+m.Name, doctor.Warn, m.Host+" is not a directory", "")
		} else {
			r.Add("mount "+m.Name, doctor.OK, fmt.Sprintf("%s -> /mnt/%s (%s)", m.Host, m.Name, m.Mode), "")
		}
	}

	// Project-specific checks.
	if l.project != nil {
		e.doctorProject(ctx, r, l, b)
	} else {
		r.Add("project", doctor.Skip, "not inside a project folder; run doctor there for project checks", "")
	}

	r.Add("version", doctor.OK, "airlock "+version.Version, "")
	if _, err := exec.LookPath("ssh"); err != nil {
		r.Add("ssh", doctor.Skip, "no ssh client; --remote will not work", "install OpenSSH")
	} else {
		r.Add("ssh", doctor.OK, "available for --remote", "")
	}
	return e.finishDoctor(r)
}

func projectRoot(l *loaded) string {
	if l.project != nil {
		return l.project.Root
	}
	return ""
}

func hasRuntime(info *backend.EngineInfo, name string) bool {
	for _, r := range info.Runtimes {
		if r == name {
			return true
		}
	}
	return false
}

func (e *env) doctorProject(ctx context.Context, r *doctor.Report, l *loaded, b backend.Backend) {
	p, cfg := l.project, &l.res.Config
	r.Add("project", doctor.OK, p.Root+" -> container "+p.ContainerName(), "")
	if p.IsWorktree() {
		r.Add("git", doctor.OK, "linked worktree of "+p.Main+": only this worktree and the shared git directory are mounted; hooks and config are read-only", "")
	} else if st, err := os.Stat(filepath.Join(p.Root, ".git")); err != nil {
		r.Add("git", doctor.Warn, "not a git repository: .git/hooks and .git/config protection and worktrees are unavailable", "git init")
	} else if !st.IsDir() {
		r.Add("git", doctor.Warn, ".git is a file (a submodule): its git directory is outside the project and not mounted, so git may not work inside the sandbox", "run airlock from the superproject")
	} else {
		r.Add("git", doctor.OK, "repository found; hooks and config are mounted read-only", "")
	}

	// Secret-looking files that are not hidden.
	if cfg.Network.Mode != "allowlist" {
		var masked *ignore.Matcher
		if f, err := os.Open(filepath.Join(p.Root, sandbox.IgnoreFile)); err == nil {
			masked, _ = ignore.New(f)
			f.Close()
		}
		if found := doctor.FindSecrets(p.Root, masked, 10); len(found) > 0 {
			r.Add("secrets", doctor.Warn,
				fmt.Sprintf("%d secret-looking file(s) are visible to the agent, and the network is not restricted to an allowlist (e.g. %s)", len(found), strings.Join(found[:min(3, len(found))], ", ")),
				"list them in .airlockignore to hide them, or set network.mode = \"allowlist\"")
		} else {
			r.Add("secrets", doctor.OK, "no unmasked secret-looking files found", "")
		}
	}

	// Credentials.
	vol := credsVolumeNameFor(p.FamilySlug(), cfg.Credentials.Scope)
	if cfg.Credentials.Scope == "shared" {
		r.Add("credentials", doctor.OK, "scope shared: volume "+vol+"; every sandbox on this machine can read the provider logins (per-project isolates them)", "")
	} else {
		r.Add("credentials", doctor.OK, "scope per-project: volume "+vol, "")
	}
	if cfg.Git.Mode == "push" {
		if os.Getenv(cfg.Git.TokenEnv) == "" {
			r.Add("git push token", doctor.Fail, "git.mode = push but $"+cfg.Git.TokenEnv+" is empty", "export "+cfg.Git.TokenEnv+" with a token scoped to the repositories you want the agent to push to")
		} else {
			r.Add("git push token", doctor.OK, "$"+cfg.Git.TokenEnv+" is set; keep its scope narrow", "")
		}
	}

	// Live checks against a running sandbox.
	name := p.ContainerName()
	info, err := b.Inspect(ctx, name)
	if err != nil || !info.Running {
		r.Add("firewall", doctor.Skip, "the sandbox is not running, so the firewall cannot be inspected", "start it with `airlock`, then run doctor again")
		return
	}
	e.doctorLive(ctx, r, b, name, cfg.Network.Mode, l)
}

func credsVolumeNameFor(slug, scope string) string {
	if scope == "per-project" {
		return sandbox.CredsVolumeBase + "-" + slug
	}
	return sandbox.CredsVolumeBase
}

func (e *env) doctorLive(ctx context.Context, r *doctor.Report, b backend.Backend, name, mode string, l *loaded) {
	run := func(user string, cmd ...string) (string, int) {
		var out bytes.Buffer
		code, err := b.Exec(ctx, name, backend.ExecOpts{Cmd: cmd, User: user, Stdout: &out, Stderr: &out})
		if err != nil {
			return err.Error(), 1
		}
		return strings.TrimSpace(out.String()), code
	}
	if mode == "open" {
		r.Add("firewall", doctor.Warn, "network mode 'open': no egress restrictions", "use open-no-lan (default) or allowlist")
	} else {
		rules, code := run("root", "iptables", "-S", "OUTPUT")
		switch {
		case code != 0:
			r.Add("firewall", doctor.Fail, "cannot read the firewall rules: "+rules, "")
		case !strings.Contains(rules, "-j REJECT"):
			r.Add("firewall", doctor.Fail, "no REJECT rule is installed: the egress policy is not enforced", "airlock stop && airlock; if it persists, check that the engine grants NET_ADMIN")
		default:
			r.Add("firewall", doctor.OK, "egress rules installed ("+mode+")", "")
		}
	}
	// A session must not be able to change the firewall.
	out, _ := run("root", sandbox.SessionWrapper, "sh", "-c", "grep CapBnd /proc/self/status")
	if mask := strings.Fields(out); len(mask) == 2 {
		var v uint64
		for _, c := range strings.ToLower(mask[1]) {
			v = v*16 + uint64(strings.IndexRune("0123456789abcdef", c))
		}
		if v&(1<<12) != 0 || v&(1<<13) != 0 {
			r.Add("capabilities", doctor.Fail, "sessions keep NET_ADMIN/NET_RAW: the firewall can be changed from inside", "recreate the sandbox: airlock rm && airlock")
		} else {
			r.Add("capabilities", doctor.OK, "sessions run without NET_ADMIN/NET_RAW", "")
		}
	}
	if _, code := run("root", "test", "-S", "/var/run/docker.sock"); code == 0 {
		r.Add("docker socket", doctor.Fail, "a Docker socket is present inside the sandbox", "remove the mount that provides it")
	} else {
		r.Add("docker socket", doctor.OK, "no Docker socket inside the sandbox", "")
	}
	// Credentials ready?
	for _, prov := range loginTargets(&l.res.Config) {
		if !validProvider(prov) {
			continue
		}
		state, code := run("root", sandbox.SessionWrapper, "pi", "auth", "check", "--provider", prov)
		if code != 0 {
			r.Add("login "+prov, doctor.Warn, firstNonEmpty(state, "not logged in"), "airlock login "+prov)
		} else {
			r.Add("login "+prov, doctor.OK, firstNonEmpty(state, "ready"), "")
		}
	}
}

func (e *env) finishDoctor(r *doctor.Report) (int, error) {
	if e.a.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		for _, c := range r.Checks {
			mark := map[doctor.Status]string{doctor.OK: "ok  ", doctor.Warn: "warn", doctor.Fail: "FAIL", doctor.Skip: "skip"}[c.Status]
			fmt.Fprintf(e.stdout, "[%s] %-22s %s\n", mark, c.Name, c.Detail)
			if c.Fix != "" {
				fmt.Fprintf(e.stdout, "       %-22s fix: %s\n", "", c.Fix)
			}
		}
	}
	if r.Failed() {
		return 1, nil
	}
	return 0, nil
}
