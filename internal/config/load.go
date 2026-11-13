package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ProjectConfigName is the per-project config file, read from the project root.
const ProjectConfigName = ".airlock.toml"

// Flags are CLI overrides. Zero values mean "not set".
type Flags struct {
	UI        string
	Agent     string
	Network   string
	Backend   string
	Team      *bool
	Ephemeral bool
}

// LoadOptions controls Load.
type LoadOptions struct {
	// ProjectDir is the project root. Empty skips the project layer.
	ProjectDir string
	// UserConfigPath overrides the default user config location.
	UserConfigPath string
	Flags          Flags
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
}

// Result is a loaded configuration and what was noticed while loading it.
type Result struct {
	Config            Config
	UserConfigPath    string
	UserConfigExists  bool
	ProjectConfigPath string
	// ProjectFiles are the files of the projects folder that applied to this
	// project, in the order they were applied.
	ProjectFiles []string
	// ProjectsDir is the projects folder in use ("" when there is none: see
	// ProjectsDirFor). ConfigFolders are the folders a sandbox must not reach.
	ProjectsDir   string
	ConfigFolders []string
	// ProjectExtras are the packages the untrusted project config asks to
	// have preinstalled. They are layered on top of the base image.
	ProjectExtras ProjectExtras
	// Warnings are non-fatal: ignored project keys, risky mounts, and so on.
	Warnings []string
}

// ProjectExtras are the project-requested additions to the image.
type ProjectExtras struct {
	Apt  []string
	Mise []string
}

// UserConfigDir returns the directory holding the user config.
func UserConfigDir() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("APPDATA"); d != "" {
			return filepath.Join(d, "airlock")
		}
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "airlock")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "airlock")
	}
	return filepath.Join(home, ".config", "airlock")
}

// UserConfigPath returns the user config file path, honoring AIRLOCK_CONFIG.
func UserConfigPath() string {
	if p := os.Getenv("AIRLOCK_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(UserConfigDir(), "config.toml")
}

// Load merges defaults, the user config, the project config, flags and env.
func Load(opts LoadOptions) (*Result, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	res := &Result{Config: Defaults()}
	res.UserConfigPath = opts.UserConfigPath
	if res.UserConfigPath == "" {
		res.UserConfigPath = UserConfigPath()
	}
	res.ProjectsDir, _ = ProjectsDirFor(res.UserConfigPath, getenv)
	res.ConfigFolders = ConfigFolders(res.UserConfigPath, res.ProjectsDir)

	data, err := os.ReadFile(res.UserConfigPath)
	switch {
	case err == nil:
		res.UserConfigExists = true
		if err := decodeStrict(data, &res.Config); err != nil {
			return nil, fmt.Errorf("%s: %w", res.UserConfigPath, err)
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("read %s: %w", res.UserConfigPath, err)
	}
	normalize(&res.Config)

	if err := res.Config.Validate(); err != nil {
		return nil, fmt.Errorf("%s:\n%w", res.UserConfigPath, err)
	}

	if opts.ProjectDir != "" {
		root, err := filepath.Abs(opts.ProjectDir)
		if err != nil {
			return nil, err
		}
		if res.ProjectsDir != "" {
			if err := applyProjectFiles(res, res.ProjectsDir, root); err != nil {
				return nil, err
			}
		}
		res.ProjectConfigPath = filepath.Join(opts.ProjectDir, ProjectConfigName)
		pdata, err := os.ReadFile(res.ProjectConfigPath)
		switch {
		case err == nil:
			if err := applyProject(res, pdata); err != nil {
				return nil, fmt.Errorf("%s: %w", res.ProjectConfigPath, err)
			}
		case errors.Is(err, fs.ErrNotExist):
			res.ProjectConfigPath = ""
		default:
			return nil, fmt.Errorf("read %s: %w", res.ProjectConfigPath, err)
		}
	}

	applyFlags(&res.Config, opts.Flags)
	if err := applyEnv(&res.Config, getenv); err != nil {
		return nil, err
	}
	normalize(&res.Config)
	if err := res.Config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration:\n%w", err)
	}
	// The mounts, checked against the folders of this run: --config and
	// AIRLOCK_PROJECTS_DIR may name folders the validation above did not know.
	for _, m := range res.Config.Mounts {
		if err := CheckFolderReach(m.Host, fmt.Sprintf("mount %q", m.Name), res.ConfigFolders); err != nil {
			return nil, err
		}
	}
	res.Warnings = append(res.Warnings, riskyMounts(&res.Config)...)
	return res, nil
}

func decodeStrict(data []byte, into any) error {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return fmt.Errorf("unknown configuration key(s):\n%s", strict.String())
		}
		var derr *toml.DecodeError
		if errors.As(err, &derr) {
			row, col := derr.Position()
			return fmt.Errorf("line %d, column %d: %s", row, col, derr.Error())
		}
		return err
	}
	return nil
}

// normalize fills nil collections so callers never see them.
func normalize(c *Config) {
	if c.Pi.Settings == nil {
		c.Pi.Settings = map[string]any{}
	}
	if c.Env == nil {
		c.Env = map[string]Env{}
	}
	if c.Team.Models == nil {
		c.Team.Models = map[string]string{}
	}
	if c.Team.Thinking == nil {
		c.Team.Thinking = map[string]string{}
	}
	for i := range c.Mounts {
		c.Mounts[i].Host = ExpandHome(c.Mounts[i].Host)
	}
	c.AgentsMDAppend = ExpandHome(c.AgentsMDAppend)
}

// projectAllowed lists the only keys a project config may set.
var projectAllowed = map[string]bool{
	"sandbox.memory":   true,
	"sandbox.cpus":     true,
	"sandbox.pids":     true,
	"sandbox.ui":       true,
	"team.enabled":     true,
	"pi.default_model": true,
	"extras.apt":       true,
	"extras.mise":      true,
	"network.mode":     true,
	"image.dockerfile": true,
}

// applyProject merges the untrusted project config. Everything outside the
// allowlist is ignored and reported; limits may only be tightened.
func applyProject(res *Result, data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		var derr *toml.DecodeError
		if errors.As(err, &derr) {
			row, col := derr.Position()
			return fmt.Errorf("line %d, column %d: %s", row, col, derr.Error())
		}
		return err
	}
	cfg := &res.Config
	var ignored []string
	warn := func(format string, a ...any) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: ", ProjectConfigName)+fmt.Sprintf(format, a...))
	}

	for _, section := range sortedKeys(raw) {
		tbl, ok := raw[section].(map[string]any)
		if !ok {
			ignored = append(ignored, section)
			continue
		}
		for _, key := range sortedKeys(tbl) {
			dotted := section + "." + key
			if !projectAllowed[dotted] {
				ignored = append(ignored, dotted)
				continue
			}
			if err := applyProjectKey(res, dotted, tbl[key], warn); err != nil {
				return err
			}
		}
	}
	if len(ignored) > 0 {
		warn("ignored %s (a project config may not set these; put them in your user config)", strings.Join(ignored, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid project configuration:\n%w", err)
	}
	// The project extras end up in generated Dockerfile lines, so they get
	// the same strict checks as the user's own.
	check := Config{Extras: Extras{Apt: res.ProjectExtras.Apt, Mise: res.ProjectExtras.Mise}}
	var errs []error
	for _, a := range check.Extras.Apt {
		if !aptRe.MatchString(a) {
			errs = append(errs, fmt.Errorf("extras.apt: %q is not a Debian package name", a))
		}
	}
	for _, m := range check.Extras.Mise {
		if !miseRe.MatchString(m) {
			errs = append(errs, fmt.Errorf("extras.mise: %q is not a mise tool spec such as node@22", m))
		}
	}
	return errors.Join(errs...)
}

func applyProjectKey(res *Result, key string, v any, warn func(string, ...any)) error {
	cfg := &res.Config
	bad := func(want string) error { return fmt.Errorf("%s must be %s", key, want) }
	switch key {
	case "sandbox.ui":
		s, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		cfg.Sandbox.UI = s
	case "pi.default_model":
		s, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		cfg.Pi.DefaultModel = s
	case "team.enabled":
		b, ok := v.(bool)
		if !ok {
			return bad("a boolean")
		}
		cfg.Team.Enabled = b
	case "image.dockerfile":
		s, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		cfg.Image.Dockerfile = s
	case "extras.apt", "extras.mise":
		list, err := stringList(v)
		if err != nil {
			return bad("a list of strings")
		}
		if key == "extras.apt" {
			res.ProjectExtras.Apt = list
		} else {
			res.ProjectExtras.Mise = list
		}
	case "network.mode":
		s, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		if NetworkStrictness(s) < 0 {
			return fmt.Errorf("network.mode: %q is not one of %s", s, strings.Join(NetworkModes, ", "))
		}
		switch {
		case NetworkStrictness(s) > NetworkStrictness(cfg.Network.Mode):
			cfg.Network.Mode = s
		case NetworkStrictness(s) < NetworkStrictness(cfg.Network.Mode):
			warn("network.mode = %q would loosen the policy (%q); ignored", s, cfg.Network.Mode)
		}
	case "sandbox.memory":
		s, ok := v.(string)
		if !ok {
			return bad("a string like \"4g\"")
		}
		want, err := ParseSize(s)
		if err != nil {
			return fmt.Errorf("sandbox.memory: %w", err)
		}
		have, _ := ParseSize(cfg.Sandbox.Memory)
		if want < have {
			cfg.Sandbox.Memory = s
		} else if want > have {
			warn("sandbox.memory = %q exceeds the user limit (%s); ignored", s, cfg.Sandbox.Memory)
		}
	case "sandbox.cpus":
		f, ok := toFloat(v)
		if !ok {
			return bad("a number")
		}
		if f < cfg.Sandbox.CPUs {
			cfg.Sandbox.CPUs = f
		} else if f > cfg.Sandbox.CPUs {
			warn("sandbox.cpus = %v exceeds the user limit (%v); ignored", f, cfg.Sandbox.CPUs)
		}
	case "sandbox.pids":
		n, ok := v.(int64)
		if !ok {
			return bad("an integer")
		}
		if n < cfg.Sandbox.Pids {
			cfg.Sandbox.Pids = n
		} else if n > cfg.Sandbox.Pids {
			warn("sandbox.pids = %d exceeds the user limit (%d); ignored", n, cfg.Sandbox.Pids)
		}
	}
	return nil
}

func applyFlags(c *Config, f Flags) {
	if f.UI != "" {
		c.Sandbox.UI = f.UI
	}
	if f.Agent != "" && f.Agent != c.Agent.Kind {
		// The configured version belongs to the configured agent.
		c.Agent.Kind, c.Agent.Version = f.Agent, "pinned"
	}
	if f.Network != "" {
		c.Network.Mode = f.Network
	}
	if f.Backend != "" {
		c.Sandbox.Backend = f.Backend
	}
	if f.Team != nil {
		c.Team.Enabled = *f.Team
	}
	if f.Ephemeral {
		c.Sandbox.Lifetime = "ephemeral"
	}
}

func applyEnv(c *Config, getenv func(string) string) error {
	str := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	str("AIRLOCK_SANDBOX_UI", &c.Sandbox.UI)
	str("AIRLOCK_SANDBOX_LIFETIME", &c.Sandbox.Lifetime)
	str("AIRLOCK_SANDBOX_BACKEND", &c.Sandbox.Backend)
	str("AIRLOCK_SANDBOX_MEMORY", &c.Sandbox.Memory)
	str("AIRLOCK_NETWORK_MODE", &c.Network.Mode)
	str("AIRLOCK_CREDENTIALS_SCOPE", &c.Credentials.Scope)
	str("AIRLOCK_GIT_MODE", &c.Git.Mode)
	str("AIRLOCK_GIT_TOKEN_ENV", &c.Git.TokenEnv)
	str("AIRLOCK_PI_DEFAULT_MODEL", &c.Pi.DefaultModel)
	if v := getenv("AIRLOCK_SANDBOX_CPUS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("AIRLOCK_SANDBOX_CPUS: %w", err)
		}
		c.Sandbox.CPUs = f
	}
	if v := getenv("AIRLOCK_SANDBOX_PIDS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("AIRLOCK_SANDBOX_PIDS: %w", err)
		}
		c.Sandbox.Pids = n
	}
	if v := getenv("AIRLOCK_TEAM_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("AIRLOCK_TEAM_ENABLED: %w", err)
		}
		c.Team.Enabled = b
	}
	return nil
}

// riskyMounts warns about mounts that expose credentials.
func riskyMounts(c *Config) []string {
	var out []string
	home, _ := os.UserHomeDir()
	sensitive := []string{".ssh", ".aws", ".gnupg", ".kube", ".docker", ".config/gcloud", ".azure", ".netrc", ".pi"}
	for _, m := range c.Mounts {
		clean := filepath.Clean(m.Host)
		for _, s := range sensitive {
			if home != "" && (clean == filepath.Join(home, s) || strings.HasPrefix(clean, filepath.Join(home, s)+string(filepath.Separator))) {
				out = append(out, fmt.Sprintf("mount %q exposes ~/%s to the sandbox; this hands its credentials to the agent", m.Name, s))
			}
		}
	}
	return out
}

// ParseSize parses sizes like 512m or 8g into bytes. A bare number is bytes.
func ParseSize(s string) (int64, error) {
	if !memoryRe.MatchString(s) {
		return 0, fmt.Errorf("%q is not a size like 512m or 8g", s)
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	}
	digits := strings.TrimRight(s, "kKmMgG")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func stringList(v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("not a list")
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, errors.New("not a string")
		}
		out = append(out, s)
	}
	return out, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
