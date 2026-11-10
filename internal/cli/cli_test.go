package cli

import (
	"reflect"
	"testing"

	"airlock/internal/config"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		argv    []string
		command string
		path    string
		rest    []string
	}{
		{nil, "start", "", nil},
		{[]string{"/some/project"}, "start", "/some/project", nil},
		{[]string{"shell", "../p"}, "shell", "../p", nil},
		{[]string{"exec", "--", "ls", "-la"}, "exec", "", []string{"ls", "-la"}},
		{[]string{"exec", "/p", "--", "make", "--verbose"}, "exec", "/p", []string{"make", "--verbose"}},
		{[]string{"login", "anthropic", "opencode-go"}, "login", "", []string{"anthropic", "opencode-go"}},
		{[]string{"config", "init"}, "config", "", []string{"init"}},
		{[]string{"./shell"}, "start", "./shell", nil}, // a directory called "shell"
	}
	for _, c := range cases {
		a, err := parseArgs(c.argv)
		if err != nil {
			t.Errorf("%v: %v", c.argv, err)
			continue
		}
		if a.command != c.command || a.path != c.path || !reflect.DeepEqual(a.rest, c.rest) {
			t.Errorf("%v: got command=%q path=%q rest=%v", c.argv, a.command, a.path, a.rest)
		}
	}
}

func TestParseArgsFlags(t *testing.T) {
	a, err := parseArgs([]string{"--team", "--ui=shell", "--network", "allowlist", "--remote", "box", "-v", "--ephemeral", "--recreate", "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.team || !a.teamSet || a.ui != "shell" || a.network != "allowlist" || a.remote != "box" || !a.verbose || !a.ephemer || !a.recreate || a.path != "p" {
		t.Errorf("flags: %+v", a)
	}
	for _, bad := range [][]string{{"--nope"}, {"--ui"}, {"a", "b"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestLoginTargets(t *testing.T) {
	c := config.Defaults()
	if got := loginTargets(&c); len(got) != 0 {
		t.Errorf("no models configured, got %v", got)
	}
	c.Pi.DefaultModel = "anthropic/claude-opus-5-5"
	c.Team.Models = map[string]string{"scout": "opencode-go/some-model", "lead": "anthropic/x", "scribe": "claude-bridge/claude-haiku-5-5", "plain": "nomodelprefix"}
	if got := loginTargets(&c); !reflect.DeepEqual(got, []string{"anthropic", "opencode-go"}) {
		t.Errorf("targets = %v (claude-bridge must be excluded: it uses Claude Code's login)", got)
	}
	if !usesBridge(&c) {
		t.Error("a claude-bridge team model means Claude Code login is needed")
	}
	d := config.Defaults()
	if usesBridge(&d) {
		t.Error("default config must not need Claude Code")
	}
}

func TestValidProvider(t *testing.T) {
	for _, ok := range []string{"anthropic", "opencode-go", "github-copilot", "a.b_c"} {
		if !validProvider(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a;b", "$(x)", "a/b"} {
		if validProvider(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"plain": "plain", "a b": "'a b'", "it's": `'it'\''s'`, "": "''", "$(x)": "'$(x)'"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
