package cli

import (
	"reflect"
	"testing"

	"github.com/alexghirelli/airlock/internal/config"
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
		{[]string{"init", "/some/project"}, "init", "/some/project", nil},
		{[]string{"./shell"}, "start", "./shell", nil}, // a directory called "shell"
		{[]string{"wt", "add", "feat"}, "wt", "", []string{"add", "feat"}},
		{[]string{"wt"}, "wt", "", nil},
		{[]string{"review", "12345", "focus", "on", "SQL"}, "wt", "", []string{"review", "12345", "focus", "on", "SQL"}},
		{[]string{"wt", "review", "https://g.example.org/r/c/core/+/12345/3"}, "wt", "", []string{"review", "https://g.example.org/r/c/core/+/12345/3"}},
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

func TestParseArgsWorktreeFlags(t *testing.T) {
	a, err := parseArgs([]string{"wt", "add", "feat", "-b", "release/1.2", "--from=origin/main", "--no-attach"})
	if err != nil {
		t.Fatal(err)
	}
	if a.command != "wt" || !reflect.DeepEqual(a.rest, []string{"add", "feat"}) || a.branch != "release/1.2" || a.from != "origin/main" || !a.noAttach {
		t.Errorf("flags: %+v", a)
	}
	if _, err := parseArgs([]string{"wt", "add", "x", "-b"}); err == nil {
		t.Error("-b without a value accepted")
	}
}

func TestWorktreeIsolationDefaultsAndFlags(t *testing.T) {
	cases := []struct {
		mode   string
		flags  []string
		review bool
		want   bool
	}{
		{"none", nil, false, false},
		{"none", nil, true, false}, // the default: everything in one sandbox
		{"reviews", nil, false, false},
		{"reviews", nil, true, true},
		{"all", nil, false, true},
		{"all", nil, true, true},
		{"none", []string{"--isolated"}, true, true},
		{"all", []string{"--shared"}, true, false},
		{"reviews", []string{"--shared"}, true, false},
		{"reviews", []string{"--isolated"}, false, true},
	}
	for _, c := range cases {
		a, err := parseArgs(append([]string{"wt", "add", "x"}, c.flags...))
		if err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{}
		cfg.Sandbox.WorktreeIsolation = c.mode
		if got := (&env{a: a}).isolate(cfg, c.review); got != c.want {
			t.Errorf("mode %q flags %v review=%v: isolated = %v, want %v", c.mode, c.flags, c.review, got, c.want)
		}
	}
	if def := config.Defaults(); def.Sandbox.WorktreeIsolation != "none" {
		t.Errorf("default worktree_isolation = %q, want none", def.Sandbox.WorktreeIsolation)
	}
}

func TestParseArgsFresh(t *testing.T) {
	a, err := parseArgs([]string{"wt", "open", "rate-limit", "--fresh"})
	if err != nil || !a.fresh {
		t.Errorf("--fresh: %+v, %v", a, err)
	}
	if a, err = parseArgs([]string{"wt", "open", "rate-limit"}); err != nil || a.fresh {
		t.Errorf("without --fresh: %+v, %v", a, err)
	}
}

func TestParseArgsAttachFlags(t *testing.T) {
	a, err := parseArgs([]string{"review", "12345", "--attach"})
	if err != nil || !a.attach || a.noAttach {
		t.Errorf("--attach: %+v, %v", a, err)
	}
	a, err = parseArgs([]string{"review", "12345", "--no-attach"})
	if err != nil || a.attach || !a.noAttach {
		t.Errorf("--no-attach: %+v, %v", a, err)
	}
}

func TestParseArgsInbox(t *testing.T) {
	a, err := parseArgs([]string{"inbox", "--watch"})
	if err != nil || a.command != "inbox" || !a.watch || a.json {
		t.Errorf("inbox --watch: %+v, %v", a, err)
	}
	a, err = parseArgs([]string{"inbox", "--json"})
	if err != nil || a.command != "inbox" || a.watch || !a.json {
		t.Errorf("inbox --json: %+v, %v", a, err)
	}
	if a, err = parseArgs([]string{"--remote", "box", "inbox", "--watch"}); err != nil || a.remote != "box" || !a.watch {
		t.Errorf("--remote with inbox --watch: %+v, %v", a, err)
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
