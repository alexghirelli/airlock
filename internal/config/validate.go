package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	memoryRe  = regexp.MustCompile(`^[1-9][0-9]*[kmgKMG]?$`)
	aptRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(=[A-Za-z0-9.+:~-]+)?$`)
	miseRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@:_./+-]*$`)
	domainRe  = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?$`)
	// A version goes into a shell command line at install time: digits, dots and
	// the usual pre-release characters only.
	agentVersionRe = regexp.MustCompile(`^(pinned|latest|[0-9][0-9A-Za-z.+-]*)$`)
	pkgRe          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@:_./+~#-]*$`)
)

func requiresClaudeCode(pkg string) bool {
	return strings.Contains(strings.ToLower(pkg), "claude-bridge")
}

func oneOf(field, v string, allowed []string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("%s: %q is not one of %s", field, v, strings.Join(allowed, ", "))
}

// Validate checks the merged configuration. It returns every problem found.
func (c *Config) Validate() error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	add(oneOf("sandbox.lifetime", c.Sandbox.Lifetime, Lifetimes))
	add(oneOf("sandbox.ui", c.Sandbox.UI, UIs))
	add(oneOf("sandbox.backend", c.Sandbox.Backend, Backends))
	add(oneOf("network.mode", c.Network.Mode, NetworkModes))
	add(oneOf("credentials.scope", c.Credentials.Scope, CredScopes))
	add(oneOf("git.mode", c.Git.Mode, GitModes))
	add(oneOf("sandbox.worktree_isolation", c.Sandbox.WorktreeIsolation, WorktreeIsolations))
	add(oneOf("extras.claude_code", c.Extras.ClaudeCode, ClaudeModes))
	add(oneOf("agent.kind", c.Agent.Kind, AgentKinds))
	if v := c.Agent.Version; v != "" && !agentVersionRe.MatchString(v) {
		add(fmt.Errorf("agent.version: %q is not pinned, latest or a version such as 2.1.322", v))
	}
	if len(c.Notify.Command) > 0 && c.Notify.Command[0] == "" {
		add(errors.New("notify.command must start with a program name"))
	}
	if c.Team.Enabled && c.Agent.Kind != "pi" {
		add(errors.New("team.enabled needs agent.kind = \"pi\": the team runs on Pi"))
	}

	if !memoryRe.MatchString(c.Sandbox.Memory) {
		add(fmt.Errorf("sandbox.memory: %q is not a size like 512m or 8g", c.Sandbox.Memory))
	}
	if c.Sandbox.CPUs <= 0 {
		add(errors.New("sandbox.cpus must be greater than 0"))
	}
	if c.Sandbox.Pids <= 0 {
		add(errors.New("sandbox.pids must be greater than 0"))
	}
	if s := c.Sandbox.IdleStop; s != "" {
		if d, err := time.ParseDuration(s); err != nil || d < MinIdleStop {
			add(fmt.Errorf("sandbox.idle_stop: %q is not a duration of at least %s, such as \"2h\" (empty = never)", s, MinIdleStop))
		}
	}
	if d := c.Sandbox.WorktreesDir; d != "" {
		if err := validRelDir(d); err != nil {
			add(fmt.Errorf("sandbox.worktrees_dir: %w", err))
		}
	}
	if d := c.Image.Dockerfile; d != "" {
		if err := validRelDir(d); err != nil {
			add(fmt.Errorf("image.dockerfile: %w", err))
		}
	}

	for _, d := range c.Network.Allow {
		if !domainRe.MatchString(strings.ToLower(d)) {
			add(fmt.Errorf("network.allow: %q is not a domain name", d))
		}
	}
	for _, cidr := range c.Network.AllowCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			add(fmt.Errorf("network.allow_cidrs: %q is not a CIDR", cidr))
		}
	}

	if c.Git.Mode == "push" && c.Git.TokenEnv == "" {
		add(errors.New("git.token_env must be set when git.mode = \"push\""))
	}
	for key, v := range map[string]string{"git.gerrit_remote": c.Git.GerritRemote, "git.review_remote": c.Git.ReviewRemote} {
		if strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\n") {
			add(fmt.Errorf("%s: %q is not a remote name", key, v))
		}
	}
	if c.Git.TokenEnv != "" && !envNameRe.MatchString(c.Git.TokenEnv) {
		add(fmt.Errorf("git.token_env: %q is not an environment variable name", c.Git.TokenEnv))
	}
	for _, h := range c.Git.Hosts {
		if !hostRe.MatchString(h) {
			add(fmt.Errorf("git.hosts: %q is not a host name", h))
		}
	}

	for _, p := range c.Pi.Packages {
		if !pkgRe.MatchString(p) {
			add(fmt.Errorf("pi.packages: %q contains unsupported characters", p))
		}
	}
	if v := c.Pi.Version; v != "latest-pinned" && !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`).MatchString(v) {
		add(fmt.Errorf("pi.version: %q is neither \"latest-pinned\" nor an exact version", v))
	}
	for _, a := range c.Extras.Apt {
		if !aptRe.MatchString(a) {
			add(fmt.Errorf("extras.apt: %q is not a Debian package name", a))
		}
	}
	for _, m := range c.Extras.Mise {
		if !miseRe.MatchString(m) {
			add(fmt.Errorf("extras.mise: %q is not a mise tool spec such as node@22", m))
		}
	}

	for name, e := range c.Env {
		if !envNameRe.MatchString(name) {
			add(fmt.Errorf("env: %q is not an environment variable name", name))
		}
		if strings.HasPrefix(name, "AIRLOCK_") {
			add(fmt.Errorf("env: %q is reserved for airlock's own settings", name))
		}
		switch {
		case e.Value != "" && e.FromHostEnv != "":
			add(fmt.Errorf("env.%s: set either value or from_host_env, not both", name))
		case e.Value == "" && e.FromHostEnv == "":
			add(fmt.Errorf("env.%s: set value or from_host_env", name))
		case e.FromHostEnv != "" && !envNameRe.MatchString(e.FromHostEnv):
			add(fmt.Errorf("env.%s.from_host_env: %q is not an environment variable name", name, e.FromHostEnv))
		case strings.ContainsRune(e.Value, 0):
			add(fmt.Errorf("env.%s.value contains a NUL byte", name))
		}
	}

	seen := map[string]bool{}
	for i, m := range c.Mounts {
		where := fmt.Sprintf("mount[%d]", i)
		if !nameRe.MatchString(m.Name) {
			add(fmt.Errorf("%s.name: %q must match %s", where, m.Name, nameRe))
			continue
		}
		if seen[m.Name] {
			add(fmt.Errorf("%s.name: duplicate mount name %q", where, m.Name))
		}
		seen[m.Name] = true
		add(oneOf(where+".mode", m.Mode, MountModes))
		if m.Host == "" {
			add(fmt.Errorf("%s.host is required", where))
		} else if err := checkMountHost(m.Host); err != nil {
			add(fmt.Errorf("%s.host: %w", where, err))
		}
		if m.Skills != "" {
			if err := validRelDir(m.Skills); err != nil {
				add(fmt.Errorf("%s.skills: %w", where, err))
			}
		}
		if strings.ContainsAny(m.Description, "\x00") {
			add(fmt.Errorf("%s.description contains a NUL byte", where))
		}
	}

	seen = map[string]bool{}
	for i, s := range c.MCP {
		where := fmt.Sprintf("mcp[%d]", i)
		if !nameRe.MatchString(s.Name) {
			add(fmt.Errorf("%s.name: %q must match %s", where, s.Name, nameRe))
			continue
		}
		if seen[s.Name] {
			add(fmt.Errorf("%s.name: duplicate server name %q", where, s.Name))
		}
		seen[s.Name] = true
		switch {
		case s.URL != "" && s.Command != "":
			add(fmt.Errorf("%s: set either url (remote server) or command (local server), not both", where))
		case s.URL == "" && s.Command == "":
			add(fmt.Errorf("%s: set url (remote server) or command (local server)", where))
		case s.URL != "" && !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://"):
			add(fmt.Errorf("%s.url: %q must be an http(s) URL", where, s.URL))
		}
		if s.URL != "" && (len(s.Args) > 0 || len(s.Env) > 0 || len(s.EnvFromHost) > 0) {
			add(fmt.Errorf("%s: args, env and env_from_host only apply to command servers", where))
		}
		if s.Command != "" && len(s.Headers) > 0 {
			add(fmt.Errorf("%s: headers only apply to url servers", where))
		}
		if strings.ContainsAny(s.Command, "\x00\n\r") {
			add(fmt.Errorf("%s.command contains control characters", where))
		}
		for _, a := range s.Args {
			if strings.ContainsAny(a, "\x00") {
				add(fmt.Errorf("%s.args: an argument contains a NUL byte", where))
			}
		}
		for k, v := range s.Env {
			if !envNameRe.MatchString(k) || strings.ContainsAny(v, "\x00") {
				add(fmt.Errorf("%s.env: %q is not a valid variable", where, k))
			}
		}
		for k, v := range s.EnvFromHost {
			if !envNameRe.MatchString(k) || !envNameRe.MatchString(v) {
				add(fmt.Errorf("%s.env_from_host: %s = %q must map a variable name to a host variable name", where, k, v))
			}
		}
		for h, v := range s.Headers {
			if strings.ContainsAny(h, " :\r\n") || h == "" {
				add(fmt.Errorf("%s.headers: %q is not a header name", where, h))
			}
			if !envNameRe.MatchString(v) {
				add(fmt.Errorf("%s.headers.%s: %q must name a host environment variable", where, h, v))
			}
		}
	}

	seen = map[string]bool{}
	for i, s := range c.Setup {
		where := fmt.Sprintf("setup[%d]", i)
		if !nameRe.MatchString(s.Name) {
			add(fmt.Errorf("%s.name: %q must match %s", where, s.Name, nameRe))
			continue
		}
		if seen[s.Name] {
			add(fmt.Errorf("%s.name: duplicate setup name %q", where, s.Name))
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Run) == "" {
			add(fmt.Errorf("%s.run is required", where))
		}
		if strings.ContainsRune(s.Run, 0) || strings.ContainsAny(s.Creates, "\x00\n\r") {
			add(fmt.Errorf("%s contains control characters", where))
		}
		for _, w := range s.Watch {
			if strings.ContainsAny(w, "\x00\n\r") || !(strings.HasPrefix(w, "/") || w == "~" || strings.HasPrefix(w, "~/")) {
				add(fmt.Errorf("%s.watch: %q must be an absolute path inside the sandbox (or start with ~/)", where, w))
			}
		}
	}

	for _, err := range runnerErrors(c.Runners) {
		add(err)
	}

	for role, lvl := range c.Team.Thinking {
		add(oneOf("team.thinking."+role, lvl, Thinkings))
	}
	for role := range c.Team.Models {
		if !nameRe.MatchString(role) {
			add(fmt.Errorf("team.models: %q is not a role name", role))
		}
	}
	for _, r := range c.Team.DefaultRoles {
		if !nameRe.MatchString(r) || r == "lead" {
			add(fmt.Errorf("team.default_roles: %q is not a valid specialist role", r))
		}
	}

	return errors.Join(errs...)
}

// validRelDir rejects absolute paths and anything that escapes its base.
func validRelDir(p string) error {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return fmt.Errorf("%q must be relative", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q must not leave its parent directory", p)
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return fmt.Errorf("%q contains control characters", p)
	}
	return nil
}

// checkMountHost refuses host paths that would hand the sandbox control of the
// host: the filesystem root, container runtime sockets and the like.
func checkMountHost(host string) error {
	p := ExpandHome(host)
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%q must be an absolute path (or start with ~)", host)
	}
	clean := filepath.Clean(p)
	if runtime.GOOS != "windows" && clean == "/" {
		return errors.New("mounting the filesystem root is not allowed")
	}
	base := strings.ToLower(filepath.Base(clean))
	if strings.HasSuffix(base, ".sock") || strings.Contains(strings.ToLower(clean), "docker.sock") ||
		strings.Contains(strings.ToLower(clean), "podman.sock") || strings.Contains(strings.ToLower(clean), "containerd.sock") {
		return errors.New("mounting a container runtime socket is not allowed")
	}
	if clean == "/var/run" || clean == "/run" || clean == "/var/run/docker" || clean == "/run/docker" {
		return errors.New("mounting the runtime directory is not allowed")
	}
	if home, err := os.UserHomeDir(); err == nil && clean == filepath.Clean(home) {
		return errors.New("mounting your entire home directory is not allowed; mount a subfolder")
	}
	return nil
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
