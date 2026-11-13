package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/alexghirelli/airlock"
	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/version"
	"github.com/alexghirelli/airlock/internal/worktree"
)

// Names shared by all sandboxes on a machine.
const (
	NetworkName     = "airlock"
	ToolsVolume     = "airlock-tools"
	CredsVolumeBase = "airlock-creds"
)

// Capabilities kept in the container. Everything else is dropped. NET_ADMIN is
// only for the entrypoint's firewall and is removed from the bounding set
// before the unprivileged user (and thus `sudo`) ever runs; the rest let
// sudo/apt/chown work. See docs/security.md for why each is there.
var keptCaps = []string{
	"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL",
	"SETGID", "SETUID", "SETPCAP", "AUDIT_WRITE", "NET_ADMIN",
}

// Host describes the machine airlock runs on.
type Host struct {
	UID, GID int
	Getenv   func(string) string
	// SELinux is true when the engine enforces SELinux (bind mounts then need
	// labels disabled to be readable).
	SELinux bool
	// GitConfig returns a git setting as seen from dir ("" if unset).
	GitConfig func(dir, key string) string
}

// DefaultHost describes the current machine.
func DefaultHost() Host {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 { // Windows: ownership is not mapped; use the image default
		uid, gid = 1000, 1000
	}
	return Host{UID: uid, GID: gid, Getenv: os.Getenv, GitConfig: gitConfig}
}

func gitConfig(dir, key string) string {
	cmd := exec.Command("git", "-C", dir, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Plan is everything needed to create and start a project's sandbox.
type Plan struct {
	Project  *Project
	Spec     backend.Spec
	Files    []backend.File
	Warnings []string
	// Policy is a hash of the settings that are fixed at creation time. When
	// it changes for an existing sandbox, the sandbox must be recreated.
	Policy string
	// BaseImage is the airlock base image, ProjectImage the per-project image
	// (empty when the base image is used as is).
	BaseImage    string
	ProjectImage string
}

// PlanInput carries the images chosen by the caller.
type PlanInput struct {
	Project *Project
	Config  *config.Result
	Host    Host
	// Image is the image the sandbox runs (base or project image).
	Image string
}

// BuildPlan derives the container spec, the runtime manifest and the files to
// inject from the project and the merged configuration. It does no engine I/O.
func BuildPlan(in PlanInput) (*Plan, error) {
	p, res, host := in.Project, in.Config, in.Host
	c := &res.Config
	if host.Getenv == nil {
		host.Getenv = os.Getenv
	}
	plan := &Plan{Project: p, Warnings: append([]string(nil), res.Warnings...)}
	warn := func(format string, a ...any) { plan.Warnings = append(plan.Warnings, fmt.Sprintf(format, a...)) }

	// ---- environment (fixed at creation: the agent cannot change it) ----
	env := map[string]string{
		"AIRLOCK_UID":               fmt.Sprint(host.UID),
		"AIRLOCK_GID":               fmt.Sprint(host.GID),
		"AIRLOCK_NET_MODE":          c.Network.Mode,
		"AIRLOCK_ALLOW_CIDRS":       strings.Join(c.Network.AllowCIDRs, ","),
		"AIRLOCK_PROJECT_DIR":       p.Path,
		"AIRLOCK_GIT_MODE":          c.Git.Mode,
		"MISE_TRUSTED_CONFIG_PATHS": p.Path,
	}
	secret := map[string]bool{}
	hostEnv := func(name string) (string, bool) {
		v := host.Getenv(name)
		return v, v != ""
	}
	if c.Git.Mode == "push" {
		tok, ok := hostEnv(c.Git.TokenEnv)
		if !ok {
			return nil, fmt.Errorf("git.mode is \"push\" but the host environment variable %s is empty or unset", c.Git.TokenEnv)
		}
		env["AIRLOCK_GIT_TOKEN"] = tok
		secret["AIRLOCK_GIT_TOKEN"] = true
	}
	for _, name := range sortedKeys(c.Env) {
		if lit := c.Env[name].Value; lit != "" {
			env[name] = lit // not a secret: it is part of the config, so it counts for the policy hash
			continue
		}
		src := c.Env[name].FromHostEnv
		v, ok := hostEnv(src)
		if !ok {
			warn("env %s: host variable %s is not set; skipped", name, src)
			continue
		}
		env[name] = v
		secret[name] = true
	}

	// ---- mounts ----
	var binds []backend.Bind
	tmpfs := map[string]string{"/run": "rw,nosuid,nodev,mode=0755,size=64m"}
	binds = append(binds, backend.Bind{Source: p.Root, Target: p.Path})
	if p.IsWorktree() {
		// A worktree sees only itself and the repository's git directory,
		// which its .git file points to by absolute path.
		gd := p.GitDir()
		binds = append(binds, backend.Bind{Source: gd, Target: containerPath(gd)})
	} else if wt := strings.Trim(filepath.ToSlash(c.Sandbox.WorktreesDir), "/"); wt != "" {
		// Worktrees in the isolated folder have a sandbox of their own: hide
		// them here, so no sandbox can read or change another's files. The
		// worktrees next to the folder share this sandbox and stay visible.
		tmpfs[path.Join(p.Path, wt, worktree.IsolatedDir)] = "ro,nosuid,nodev,noexec,size=4k"
	}
	worktreeConfig := host.gitConfig(p.RepoRoot(), "extensions.worktreeConfig") == "true"
	for _, g := range gitProtectedPaths(p, worktreeConfig) {
		binds = append(binds, backend.Bind{Source: g.host, Target: g.container, ReadOnly: true})
	}
	var mountInfos []mountInfo
	for _, m := range c.Mounts {
		target := "/mnt/" + m.Name
		if _, err := os.Stat(m.Host); err != nil {
			warn("mount %q: %s does not exist on this machine; skipped", m.Name, m.Host)
			continue
		}
		binds = append(binds, backend.Bind{Source: m.Host, Target: target, ReadOnly: m.Mode == "ro"})
		mi := mountInfo{Name: m.Name, Path: target, Mode: m.Mode, Description: strings.TrimSpace(m.Description)}
		if m.Skills != "" {
			mi.Skills = m.Skills
			mi.SkillsPath = path.Join(target, filepath.ToSlash(m.Skills))
		}
		mountInfos = append(mountInfos, mi)
	}
	masks, maskWarn := ignoreMasks(p)
	plan.Warnings = append(plan.Warnings, maskWarn...)

	credsVol := CredsVolumeBase
	var credsLabels map[string]string
	if c.Credentials.Scope == "per-project" {
		credsVol = CredsVolumeBase + "-" + p.FamilySlug()
		// What lets `airlock gc` tell that the project is gone.
		credsLabels = map[string]string{backend.LabelKind: backend.KindCredentials, backend.LabelProject: p.RepoRoot()}
	}
	volumes := []backend.VolumeMount{
		{Name: credsVol, Target: "/creds", Labels: credsLabels},
		{Name: ToolsVolume, Target: "/home/agent/.local/share/mise"},
	}

	// ---- MCP ----
	mcp := map[string]any{}
	for _, s := range c.MCP {
		entry := map[string]any{}
		if s.Command != "" {
			entry["command"] = s.Command
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			childEnv := map[string]string{}
			for k, v := range s.Env {
				childEnv[k] = v
			}
			for k, hostName := range s.EnvFromHost {
				v, ok := hostEnv(hostName)
				if !ok {
					warn("mcp %s: host variable %s is not set; %s skipped", s.Name, hostName, k)
					continue
				}
				env[hostName] = v
				secret[hostName] = true
				childEnv[k] = "${" + hostName + "}"
			}
			if len(childEnv) > 0 {
				entry["env"] = childEnv
			}
		} else {
			entry["url"] = s.URL
		}
		if s.Description != "" {
			entry["description"] = s.Description
		}
		if len(s.Headers) > 0 {
			hdr := map[string]string{}
			for h, envName := range s.Headers {
				v, ok := hostEnv(envName)
				if !ok {
					warn("mcp %s: host variable %s is not set; header %s skipped", s.Name, envName, h)
					continue
				}
				env[envName] = v
				secret[envName] = true
				hdr[h] = "${" + envName + "}"
			}
			if len(hdr) > 0 {
				entry["headers"] = hdr
			}
		}
		mcp[s.Name] = entry
	}

	// ---- manifest ----
	man := manifest{Version: 1, ProjectDir: p.Path, UI: c.Sandbox.UI, Mise: dedupe(append(append([]string(nil), c.Extras.Mise...), res.ProjectExtras.Mise...))}
	man.Git.Mode = c.Git.Mode
	man.Git.Hosts = c.Git.Hosts
	man.Git.Name = host.gitConfig(p.Root, "user.name")
	man.Git.Email = host.gitConfig(p.Root, "user.email")
	man.Pi.Packages = c.Pi.Packages
	man.Pi.MCP = mcp
	man.Setup = c.Setup
	man.IdleStop = c.IdleStopSeconds()
	man.Pi.Settings = piSettings(c)
	man.ClaudeCode = c.NeedsClaudeCode()
	man.Agent.Kind = c.Agent.Kind
	man.Agent.Args = c.Agent.Args
	man.Agent.Resume = c.Agent.Resume
	man.Agent.Versions = map[string]string{"pi": c.PiVersion(), "claude": c.ClaudeVersion()}
	// What the start installs: the agent, and Claude Code when something else needs it.
	man.Agent.Install = []string{c.Agent.Kind}
	if c.Agent.Kind != "claude" && c.NeedsClaudeCode() {
		man.Agent.Install = append(man.Agent.Install, "claude")
	}
	manJSON, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}

	agents, err := renderAgentsMD(c, p, mountInfos, mcpInfos(c))
	if err != nil {
		return nil, err
	}
	plan.Files = []backend.File{
		{Path: "/etc/airlock/manifest.json", Data: manJSON},
		{Path: "/etc/airlock/AGENTS.md", Data: agents},
		{Path: "/etc/airlock/models.env", Data: []byte(modelsEnv(c))},
		{Path: "/etc/airlock/pi.args", Data: []byte(piArgs(c, mountInfos))},
		{Path: "/etc/airlock/claude-mcp.json", Data: claudeMCP(mcp)},
	}
	if c.Agents.Concise {
		plan.Files = append(plan.Files, backend.File{Path: conciseSkillPath + "/SKILL.md", Data: []byte(mustAsset("image/skills/concise/SKILL.md"))})
	}
	profiles, err := teamProfiles(c)
	if err != nil {
		return nil, err
	}
	plan.Files = append(plan.Files, profiles...)

	// ---- spec ----
	labels := map[string]string{
		backend.LabelManaged:  "true",
		backend.LabelProject:  p.Root,
		backend.LabelSlug:     p.Slug,
		backend.LabelBackend:  c.Sandbox.Backend,
		backend.LabelVersion:  version.Version,
		backend.LabelLifetime: c.Sandbox.Lifetime,
	}
	spec := backend.Spec{
		Name:        p.ContainerName(),
		Image:       in.Image,
		Hostname:    p.ContainerName(),
		WorkDir:     p.Path,
		Labels:      labels,
		Env:         envList(env),
		Binds:       binds,
		Volumes:     volumes,
		Tmpfs:       tmpfs,
		Network:     NetworkName,
		CapAdd:      append([]string(nil), keptCaps...),
		Init:        true,
		MemoryBytes: mustSize(c.Sandbox.Memory),
		NanoCPUs:    int64(c.Sandbox.CPUs * 1e9),
		Pids:        c.Sandbox.Pids,
		AutoRemove:  c.Sandbox.Lifetime == "ephemeral",
		Cmd:         []string{"sleep", "infinity"},
	}
	if c.Sandbox.Backend == "gvisor" {
		spec.Runtime = "runsc"
	}
	if host.SELinux {
		spec.SecurityOpt = append(spec.SecurityOpt, "label=disable")
	}
	// Masks are applied at creation but kept out of the policy hash: a new
	// secret file appearing must not trigger a "recreate the sandbox?" prompt.
	plan.Policy = policyHash(spec, secret)
	spec.Binds = append(spec.Binds, masks...)
	spec.Labels[backend.LabelPolicy] = plan.Policy
	plan.Spec = spec
	return plan, nil
}

func (h Host) gitConfig(dir, key string) string {
	if h.GitConfig == nil {
		return ""
	}
	return h.GitConfig(dir, key)
}

type protectedPath struct{ host, container string }

// gitProtectedPaths returns the repository files mounted read-only so the
// agent cannot plant code that later runs on the host: hooks and config
// (core.hooksPath, core.fsmonitor, aliases, filter drivers), and, for a
// worktree, its own config.worktree (read when extensions.worktreeConfig is on).
// For the project's own sandbox, which also serves the worktrees that share it,
// that means the config.worktree of every worktree, but only while the extension
// is on: otherwise git ignores the files and protecting them would only change
// the policy each time a worktree is added.
func gitProtectedPaths(p *Project, worktreeConfig bool) []protectedPath {
	gitDir := p.GitDir()
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return nil
	}
	names := []string{"hooks", "config"}
	if p.IsWorktree() {
		if admin := worktree.AdminDir(p.Root); admin != "" {
			names = append(names, filepath.Join("worktrees", filepath.Base(admin), "config.worktree"))
		}
	} else if matches, _ := filepath.Glob(filepath.Join(gitDir, "worktrees", "*", "config.worktree")); worktreeConfig {
		for _, m := range matches {
			rel, _ := filepath.Rel(gitDir, m)
			names = append(names, rel)
		}
	}
	var out []protectedPath
	for _, name := range names {
		h := filepath.Join(gitDir, name)
		if _, err := os.Stat(h); err == nil {
			out = append(out, protectedPath{host: h, container: containerPath(h)})
		}
	}
	return out
}

type mountInfo struct {
	Name, Path, Mode, Description, Skills, SkillsPath string
}

type mcpInfo struct{ Name, Description string }

func mcpInfos(c *config.Config) []mcpInfo {
	var out []mcpInfo
	for _, s := range c.MCP {
		out = append(out, mcpInfo{Name: s.Name, Description: s.Description})
	}
	return out
}

// manifest is the runtime configuration read by the image's init script.
type manifest struct {
	Version    int    `json:"version"`
	ProjectDir string `json:"project_dir"`
	UI         string `json:"ui"`
	Git        struct {
		Name  string   `json:"name,omitempty"`
		Email string   `json:"email,omitempty"`
		Mode  string   `json:"mode"`
		Hosts []string `json:"hosts"`
	} `json:"git"`
	Pi struct {
		Settings map[string]any `json:"settings"`
		Packages []string       `json:"packages"`
		MCP      map[string]any `json:"mcp"`
	} `json:"pi"`
	Mise       []string       `json:"mise"`
	IdleStop   int            `json:"idle_stop_seconds"` // 0: the sandbox never stops by itself
	Setup      []config.Setup `json:"setup"`
	ClaudeCode bool           `json:"claude_code"`
	Agent      struct {
		Kind     string            `json:"kind"`
		Versions map[string]string `json:"versions"` // what airlock-agent-install puts in place
		Install  []string          `json:"install"`  // installed when the sandbox starts
		Args     []string          `json:"args"`
		Resume   bool              `json:"resume"` // continue the previous conversation of a directory
	} `json:"agent"`
}

// piSettings builds the overlay merged into the shared Pi settings.json: the
// user's own settings and the npm command Pi uses to install packages. Anything
// that differs per project (default model, skills from mounts) is not here; see
// piArgs.
func piSettings(c *config.Config) map[string]any {
	s := map[string]any{}
	for k, v := range c.Pi.Settings {
		s[k] = v
	}
	s["npmCommand"] = []any{"/opt/pi/shims/npm"}
	return s
}

// piArgs are the per-sandbox defaults the pi wrapper puts in front of its
// arguments: one per line, so values may contain spaces.
func piArgs(c *config.Config, mounts []mountInfo) string {
	var b strings.Builder
	if c.Agents.Concise {
		fmt.Fprintf(&b, "--skill\n%s\n", conciseSkillPath)
	}
	for _, m := range mounts {
		if m.SkillsPath != "" {
			fmt.Fprintf(&b, "--skill\n%s\n", m.SkillsPath)
		}
	}
	if m := c.Pi.DefaultModel; m != "" {
		fmt.Fprintf(&b, "--model\n%s\n", m)
	}
	if c.Agent.Kind == "pi" {
		for _, a := range c.Agent.Args {
			fmt.Fprintf(&b, "%s\n", a)
		}
	}
	return b.String()
}

// claudeMCP is the MCP configuration for Claude Code (--mcp-config): the same
// servers as Pi gets, in its format. Remote servers say their transport; the
// ${VAR} references are expanded by Claude Code from the sandbox environment.
func claudeMCP(mcp map[string]any) []byte {
	servers := map[string]any{}
	for name, v := range mcp {
		entry := map[string]any{}
		for k, val := range v.(map[string]any) {
			if k != "description" {
				entry[k] = val
			}
		}
		if _, remote := entry["url"]; remote {
			entry["type"] = "http"
		}
		servers[name] = entry
	}
	b, _ := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	return b
}

var agentsTmpl = template.Must(template.New("agents").Parse(mustAsset("image/AGENTS.md.tmpl")))

func mustAsset(name string) string {
	b, err := airlock.Assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func renderAgentsMD(c *config.Config, p *Project, mounts []mountInfo, mcp []mcpInfo) ([]byte, error) {
	append_ := ""
	if c.AgentsMDAppend != "" {
		b, err := os.ReadFile(c.AgentsMDAppend)
		if err != nil {
			return nil, fmt.Errorf("agents_md_append: %w", err)
		}
		append_ = strings.TrimSpace(string(b))
	}
	data := map[string]any{
		"ProjectDir":   p.Path,
		"Persistent":   c.Sandbox.Lifetime == "persistent",
		"WorktreesDir": strings.Trim(filepath.ToSlash(c.Sandbox.WorktreesDir), "/"),
		"IsWorktree":   p.IsWorktree(),
		"MainDir":      containerPath(p.RepoRoot()),
		"NetworkText":  networkText(c),
		"GitText":      gitText(c),
		"Mounts":       mounts,
		"MCP":          mcp,
		"Append":       append_,
	}
	if c.Agents.Concise {
		data["Concise"] = skillBody(mustAsset("image/skills/concise/SKILL.md"))
	}
	var buf bytes.Buffer
	if err := agentsTmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// conciseSkillPath is where the built-in skill is written inside the sandbox.
const conciseSkillPath = "/etc/airlock/skills/concise"

// skillBody returns a skill's text without its front matter.
func skillBody(skill string) string {
	if rest, ok := strings.CutPrefix(skill, "---\n"); ok {
		if _, body, found := strings.Cut(rest, "\n---\n"); found {
			skill = body
		}
	}
	return strings.TrimSpace(skill)
}

func networkText(c *config.Config) string {
	switch c.Network.Mode {
	case "allowlist":
		s := "Network egress goes through a filtering proxy that only allows the package registries and model providers the user configured"
		if len(c.Network.Allow) > 0 {
			s += ", plus: " + strings.Join(c.Network.Allow, ", ")
		}
		return s + ". Everything else is blocked. The user's computer and local network are not reachable."
	case "open":
		return "No network restrictions are applied by airlock. Be careful with what you connect to and send."
	}
	s := "The public internet works. The user's computer, the local network, link-local addresses and cloud metadata endpoints are blocked by a firewall you cannot change."
	if len(c.Network.AllowCIDRs) > 0 {
		s += " Exceptions the user made: " + strings.Join(c.Network.AllowCIDRs, ", ") + "."
	}
	return s
}

func gitText(c *config.Config) string {
	if c.Git.Mode == "push" {
		return "Commits are allowed (the user's `user.name` and `user.email` are set). A token for " +
			strings.Join(c.Git.Hosts, ", ") + " is available through git's credential helper, so you can push " +
			"when the task needs it. Push only to branches you created, never force-push, and never push to a " +
			"shared or protected branch unless the user explicitly asks. Never print or store the token."
	}
	return "Commits are allowed (the user's `user.name` and `user.email` are set). There are no remote credentials " +
		"in this sandbox, so you cannot push or reach private remotes: leave pushing to the user, who will pull your " +
		"commits from the project folder."
}

// ---- team ----

var roleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func modelsEnv(c *config.Config) string {
	roles := map[string]bool{"lead": true}
	for _, r := range c.Team.DefaultRoles {
		roles[r] = true
	}
	for r := range c.Team.Models {
		roles[r] = true
	}
	for r := range c.Team.Thinking {
		roles[r] = true
	}
	names := make([]string, 0, len(roles))
	for r := range roles {
		names = append(names, r)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Generated by airlock from [team.models] and [team.thinking]. Do not edit.\n")
	for _, r := range names {
		up := strings.ToUpper(strings.ReplaceAll(r, "-", "_"))
		model := c.Team.Models[r]
		if model == "" {
			model = c.Pi.DefaultModel
		}
		fmt.Fprintf(&b, "MODEL_%s=%s\n", up, shellQuote(model))
		if t := c.Team.Thinking[r]; t != "" {
			fmt.Fprintf(&b, "THINK_%s=%s\n", up, shellQuote(t))
		}
	}
	fmt.Fprintf(&b, "DEFAULT_ROLES=%s\n", shellQuote(strings.Join(c.Team.DefaultRoles, " ")))
	return b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// teamProfiles returns the user's role profiles, which add to or replace the
// built-in ones inside the sandbox.
func teamProfiles(c *config.Config) ([]backend.File, error) {
	dir := config.ExpandHome(c.Team.ProfilesDir)
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("team.profiles_dir: %w", err)
	}
	var files []backend.File
	for _, e := range entries {
		role := strings.TrimSuffix(e.Name(), ".md")
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || !roleNameRe.MatchString(role) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("team.profiles_dir: %w", err)
		}
		if len(data) > 256<<10 {
			return nil, fmt.Errorf("team.profiles_dir: %s is larger than 256 KiB", e.Name())
		}
		files = append(files, backend.File{Path: "/etc/airlock/team/profiles/" + e.Name(), Data: data})
	}
	return files, nil
}

// ---- helpers ----

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, k+"="+m[k])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func mustSize(s string) int64 {
	n, _ := config.ParseSize(s)
	return n
}

// policyHash fingerprints what is fixed when the container is created. Secret
// values are left out (only their names count), so rotating a token does not
// force a rebuild and the hash never derives from a secret.
func policyHash(s backend.Spec, secret map[string]bool) string {
	env := make([]string, 0, len(s.Env))
	for _, kv := range s.Env {
		k, _, _ := strings.Cut(kv, "=")
		if secret[k] {
			env = append(env, k)
		} else {
			env = append(env, kv)
		}
	}
	sort.Strings(env)
	v := struct {
		Image, Network, Runtime string
		Env                     []string
		Binds                   []backend.Bind
		Volumes                 []backend.VolumeMount
		Tmpfs                   map[string]string
		Caps, SecOpt            []string
		Mem, CPU, Pids          int64
		Auto                    bool
	}{s.Image, s.Network, s.Runtime, env, s.Binds, s.Volumes, s.Tmpfs, s.CapAdd, s.SecurityOpt, s.MemoryBytes, s.NanoCPUs, s.Pids, s.AutoRemove}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
