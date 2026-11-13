// Package config loads, merges and validates airlock configuration.
//
// Precedence (lowest to highest): built-in defaults, user config, settings for one
// project (trusted, in the projects folder next to the user config), project
// config (untrusted, allowlisted), CLI flags, AIRLOCK_* environment variables.
package config

import (
	"fmt"
	"time"
)

// Pinned versions of the tools baked into the image. "latest-pinned" in the
// config resolves to these.
const (
	PinnedPiVersion         = "1.5.2"
	PinnedClaudeCodeVersion = "2.1.322"
	PinnedNodeMajor         = "22"
)

// Config is the fully merged configuration.
type Config struct {
	Sandbox     Sandbox        `toml:"sandbox"`
	Network     Network        `toml:"network"`
	Credentials Credentials    `toml:"credentials"`
	Git         Git            `toml:"git"`
	Agent       Agent          `toml:"agent"`
	Pi          Pi             `toml:"pi"`
	Extras      Extras         `toml:"extras"`
	Image       Image          `toml:"image"`
	Env         map[string]Env `toml:"env"`
	Mounts      []Mount        `toml:"mount"`
	MCP         []MCP          `toml:"mcp"`
	Setup       []Setup        `toml:"setup"`
	Team        Team           `toml:"team"`
	Audit       Audit          `toml:"audit"`
	Agents      Agents         `toml:"agents"`
	Notify      Notify         `toml:"notify"`
	Runners     []Runner       `toml:"runner"`

	// AgentsMDAppend is a host path whose content is appended to the
	// generated global agent instructions.
	AgentsMDAppend string `toml:"agents_md_append"`
}

type Sandbox struct {
	Lifetime     string  `toml:"lifetime"` // persistent | ephemeral
	UI           string  `toml:"ui"`       // herdr | agent | shell (pi: older name of agent)
	Backend      string  `toml:"backend"`  // container | gvisor | microvm | vm
	Memory       string  `toml:"memory"`   // e.g. "8g"
	CPUs         float64 `toml:"cpus"`
	Pids         int64   `toml:"pids"`
	WorktreesDir string  `toml:"worktrees_dir"` // "" disables worktree support
	// WorktreeIsolation decides which worktrees made by `airlock wt` get a
	// sandbox of their own: none (all share the project's, one Herdr workspace
	// each), reviews (only `wt review`) or all.
	WorktreeIsolation string `toml:"worktree_isolation"`
	// IdleStop stops the sandbox after this long with no terminal attached and no
	// agent working ("2h"). Empty means never. airlock starts it again on demand.
	IdleStop string `toml:"idle_stop"`
	// WorktreeLink lists paths of the project root that each new worktree gets as
	// a relative symlink to the main checkout's entry: LocalSettings.php, vendor.
	// Only the user config and project settings files may set it.
	WorktreeLink []string `toml:"worktree_link"`
}

// MinIdleStop is the shortest idle_stop: a sandbox that stops after seconds would
// cut off anyone who steps away to read.
const MinIdleStop = 5 * time.Minute

// IdleStopSeconds is idle_stop in seconds, 0 when the sandbox never stops by itself.
func (c *Config) IdleStopSeconds() int {
	d, err := time.ParseDuration(c.Sandbox.IdleStop)
	if err != nil || c.Sandbox.IdleStop == "" {
		return 0
	}
	return int(d / time.Second)
}

type Network struct {
	Mode       string   `toml:"mode"`        // open-no-lan | allowlist | open
	Allow      []string `toml:"allow"`       // extra domains for allowlist mode
	AllowCIDRs []string `toml:"allow_cidrs"` // holes in the LAN block
}

type Credentials struct {
	Scope string `toml:"scope"` // shared | per-project
}

type Git struct {
	Mode     string   `toml:"mode"`      // local | push
	TokenEnv string   `toml:"token_env"` // host env var holding the token (push mode)
	Hosts    []string `toml:"hosts"`     // hosts the token is offered to (push mode)
	// GerritRemote is the remote `airlock wt review` fetches refs/changes from.
	// Empty means "gerrit" if the repository has such a remote, else "origin".
	GerritRemote string `toml:"gerrit_remote"`
	// ReviewRemote is the remote `airlock review` fetches from, for Gerrit
	// changes, pull requests and merge requests alike. Empty means: the remote a
	// change URL points at, otherwise "gerrit", "origin" and "upstream" in turn.
	ReviewRemote string `toml:"review_remote"`
}

// Agent picks the agent harness that runs in the sandbox and its version. Only Pi
// is in the image; the others are installed by their own installer into the tools
// volume the first time they are needed, so nothing proprietary is redistributed.
type Agent struct {
	Kind    string   `toml:"kind"`    // pi | claude
	Version string   `toml:"version"` // pinned | latest | an exact version
	Args    []string `toml:"args"`    // extra arguments for the agent, e.g. Claude Code's permission mode
	// Resume continues the conversation the agent had in a directory when its
	// workspace is opened again (a closed worktree, a restarted sandbox).
	Resume bool `toml:"resume"`
}

type Pi struct {
	Version      string         `toml:"version"` // latest-pinned or exact
	Packages     []string       `toml:"packages"`
	DefaultModel string         `toml:"default_model"`
	Settings     map[string]any `toml:"settings"`
}

type Extras struct {
	ClaudeCode string   `toml:"claude_code"` // auto | true | false
	Apt        []string `toml:"apt"`
	Mise       []string `toml:"mise"`
}

type Image struct {
	// Dockerfile is the project image Dockerfile, relative to the project.
	Dockerfile string `toml:"dockerfile"`
}

// Env declares an environment variable set in the sandbox: either a literal
// Value, or the content of a host variable (FromHostEnv), for secrets.
type Env struct {
	FromHostEnv string `toml:"from_host_env"`
	Value       string `toml:"value"`
}

type Mount struct {
	Name        string `toml:"name"`
	Host        string `toml:"host"`
	Mode        string `toml:"mode"` // ro | rw
	Description string `toml:"description"`
	Skills      string `toml:"skills"` // subfolder to load Pi skills from; "." = the mount root
}

// MCP is an MCP server added to Pi's MCP config: remote (url) or a local
// process started inside the sandbox (command).
type MCP struct {
	Name        string `toml:"name"`
	URL         string `toml:"url"`
	Description string `toml:"description"`
	// Headers maps a header name to the host env var holding its value (url servers).
	Headers map[string]string `toml:"headers"`
	// Command, Args and Env start a stdio server inside the sandbox. Env holds
	// literal values; EnvFromHost maps a variable name to the host env var that
	// holds its value, for secrets.
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Env         map[string]string `toml:"env"`
	EnvFromHost map[string]string `toml:"env_from_host"`
}

// Agents tunes what every agent in the sandbox is told.
type Agents struct {
	// Concise ships the built-in "concise" skill and puts its rules in the
	// generated agent instructions, so replies stay short and actionable.
	Concise bool `toml:"concise"`
}

// Setup is a command run inside the sandbox before the agent starts, to
// prepare something airlock knows nothing about (a virtualenv, node_modules...).
// It is done when its command is unchanged and the Creates path exists.
type Setup struct {
	Name    string `toml:"name" json:"name"`
	Run     string `toml:"run" json:"run"`
	Creates string `toml:"creates" json:"creates"`
	// Watch lists paths whose content decides whether the step runs again
	// (the step also reruns when Run changes or Creates is missing).
	Watch []string `toml:"watch" json:"watch"`
}

// Notify is what `airlock inbox --watch` runs when an agent needs you. It runs on
// this computer, so only the user's own config may set it, never a project's.
type Notify struct {
	// Command is an argv, run with the title and the message appended. Empty
	// means the desktop's own notifier (notify-send, osascript).
	Command []string `toml:"command"`
}

// Runner is a container started outside airlock (a docker compose service, DDEV,
// a plain container) in which the user runs a project's commands against one of
// its worktrees: `airlock wt run`. Only the user's config and the trusted
// per-project files may declare one; a project's .airlock.toml may not.
type Runner struct {
	Name string `toml:"name"`
	// Compose is a compose file, or a list of them in order (each becomes its own
	// -f), with Service. It is a string or a list of strings: use ComposeFiles.
	Compose   any    `toml:"compose"`
	Service   string `toml:"service"`   // compose service
	Container string `toml:"container"` // or a running container's name
	Mount     string `toml:"mount"`     // where the project root is mounted in the container
	User      string `toml:"user"`      // optional, passed as docker's --user
}

// ComposeFiles is the compose file list of r, as written (not expanded): nil when
// there is none. It fails when compose is neither a string nor a list of strings.
func (r Runner) ComposeFiles() ([]string, error) {
	switch v := r.Compose.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	case []any:
		files := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("compose must be a path or a list of paths")
			}
			files = append(files, s)
		}
		return files, nil
	case []string:
		return v, nil
	}
	return nil, fmt.Errorf("compose must be a path or a list of paths")
}

type Team struct {
	Enabled      bool              `toml:"enabled"`
	ProfilesDir  string            `toml:"profiles_dir"`
	DefaultRoles []string          `toml:"default_roles"`
	Models       map[string]string `toml:"models"`
	Thinking     map[string]string `toml:"thinking"`
}

type Audit struct {
	// ExtraPatterns are additional gitignore-style patterns of host-executable
	// or auto-loaded files to report on.
	ExtraPatterns []string `toml:"extra_patterns"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Sandbox: Sandbox{
			Lifetime:     "persistent",
			UI:           "herdr",
			Backend:      "container",
			Memory:       "8g",
			CPUs:         4,
			Pids:         2048,
			WorktreesDir: ".worktrees", WorktreeIsolation: "none",
		},
		Network:     Network{Mode: "open-no-lan", Allow: []string{}, AllowCIDRs: []string{}},
		Credentials: Credentials{Scope: "shared"},
		Agents:      Agents{Concise: true},
		Notify:      Notify{Command: []string{}},
		Git:         Git{Mode: "local", Hosts: []string{"github.com"}},
		Agent:       Agent{Kind: "pi", Version: "pinned", Args: []string{}, Resume: true},
		Pi:          Pi{Version: "latest-pinned", Packages: []string{}, Settings: map[string]any{}},
		Extras:      Extras{ClaudeCode: "auto", Apt: []string{}, Mise: []string{}},
		Env:         map[string]Env{},
		Audit:       Audit{ExtraPatterns: []string{}},
		Team: Team{
			DefaultRoles: []string{"reviewer", "tester", "scout", "scribe"},
			Models:       map[string]string{},
			Thinking: map[string]string{
				"lead": "high", "reviewer": "high", "tester": "medium", "scout": "medium", "scribe": "low",
			},
		},
	}
}

// Enumerations accepted by the validator.
var (
	Lifetimes    = []string{"persistent", "ephemeral"}
	UIs          = []string{"herdr", "agent", "pi", "shell"} // pi is the older name of agent
	Backends     = []string{"container", "gvisor", "microvm", "vm"}
	NetworkModes = []string{"open", "open-no-lan", "allowlist"} // loosest to strictest
	CredScopes   = []string{"shared", "per-project"}
	GitModes     = []string{"local", "push"}
	// WorktreeIsolations are the values of sandbox.worktree_isolation.
	WorktreeIsolations = []string{"none", "reviews", "all"}
	ClaudeModes        = []string{"auto", "true", "false"}
	AgentKinds         = []string{"pi", "claude"}
	MountModes         = []string{"ro", "rw"}
	Thinkings          = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
)

// NetworkStrictness orders network modes: higher is stricter.
func NetworkStrictness(mode string) int {
	for i, m := range NetworkModes {
		if m == mode {
			return i
		}
	}
	return -1
}

// PiVersion resolves the Pi version the sandbox runs to a concrete npm version.
// The image has the pinned one; any other is installed into the tools volume.
func (c *Config) PiVersion() string {
	if c.Agent.Kind == "pi" && !pinned(c.Agent.Version) {
		return c.Agent.Version
	}
	if c.Pi.Version == "" || c.Pi.Version == "latest-pinned" {
		return PinnedPiVersion
	}
	return c.Pi.Version
}

// ClaudeVersion resolves the Claude Code version to install: "latest" or an exact version.
func (c *Config) ClaudeVersion() string {
	if c.Agent.Kind == "claude" && !pinned(c.Agent.Version) {
		return c.Agent.Version
	}
	return PinnedClaudeCodeVersion
}

func pinned(v string) bool { return v == "" || v == "pinned" || v == "latest-pinned" }

// NeedsClaudeCode reports whether Claude Code must be installed in the sandbox: it
// is the agent, or something else uses it (a Pi package that goes through Claude
// Code, or extras.claude_code = "true").
func (c *Config) NeedsClaudeCode() bool {
	if c.Agent.Kind == "claude" {
		return true
	}
	switch c.Extras.ClaudeCode {
	case "true":
		return true
	case "false":
		return false
	}
	for _, p := range c.Pi.Packages {
		if requiresClaudeCode(p) {
			return true
		}
	}
	return false
}
