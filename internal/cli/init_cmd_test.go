package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// runInit runs `airlock init` through Main with HOME and the config locations in a
// temp dir, so nothing reaches the real user config.
func runInit(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AIRLOCK_CONFIG", filepath.Join(home, "config.toml"))
	var out, errb bytes.Buffer
	code := Main(append([]string{"init"}, argv...), &out, &errb)
	return code, out.String(), errb.String()
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInitWritesAndRefuses(t *testing.T) {
	dir := writeProject(t, map[string]string{"package.json": `{"engines":{"node":">=20"}}`})
	target := filepath.Join(dir, ".airlock.toml")

	code, out, _ := runInit(t, dir)
	if code != 0 {
		t.Fatalf("first run: exit %d", code)
	}
	written, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(written), `"node@22"`) {
		t.Fatalf("file not written: %v\n%s", err, written)
	}
	if !strings.Contains(out, "wrote") || !strings.Contains(out, "next: airlock") {
		t.Errorf("output = %q", out)
	}

	// A second run refuses, prints the proposal, and leaves the file alone.
	if err := os.WriteFile(target, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runInit(t, dir)
	if code != 1 {
		t.Errorf("refusal: exit %d, want 1", code)
	}
	if !strings.Contains(out, `"node@22"`) || !strings.Contains(out, "proposal") {
		t.Errorf("refusal did not print the proposal: %q", out)
	}
	if !strings.Contains(errOut, "already exists") {
		t.Errorf("stderr = %q", errOut)
	}
	if got, _ := os.ReadFile(target); string(got) != "# mine\n" {
		t.Errorf("refused run changed the file: %q", got)
	}

	// --force overwrites it.
	code, _, _ = runInit(t, dir, "--force")
	if code != 0 {
		t.Fatalf("--force: exit %d", code)
	}
	if got, _ := os.ReadFile(target); !strings.Contains(string(got), `"node@22"`) {
		t.Errorf("--force did not overwrite: %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v; want 0644", info.Mode().Perm())
	}
}

func TestInitJSONWritesNothing(t *testing.T) {
	dir := writeProject(t, map[string]string{"go.mod": "module x\n\ngo 1.27.3\n"})
	code, out, _ := runInit(t, dir, "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var rep struct {
		Config   string `json:"config"`
		Findings struct {
			Mise []struct {
				Spec string `json:"spec"`
			} `json:"mise"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(rep.Findings.Mise) != 1 || rep.Findings.Mise[0].Spec != "go@1.27.3" || !strings.Contains(rep.Config, "go@1.27.3") {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, ".airlock.toml")); err == nil {
		t.Error("--json wrote .airlock.toml")
	}
}

func TestInitNothingToSet(t *testing.T) {
	// A pinned version needs no setting: the report is printed, nothing is written.
	dir := writeProject(t, map[string]string{".nvmrc": "22\n", "package.json": "{}"})
	code, out, _ := runInit(t, dir)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "picked up by mise from .nvmrc") || !strings.Contains(out, "nothing to set") {
		t.Errorf("output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".airlock.toml")); err == nil {
		t.Error("wrote .airlock.toml with nothing to set")
	}

	empty := t.TempDir()
	code, out, _ = runInit(t, empty)
	if code != 0 || !strings.Contains(out, "nothing detected") {
		t.Errorf("empty project: exit %d, output %q", code, out)
	}
}

func TestInitRefusesSymlink(t *testing.T) {
	dir := writeProject(t, map[string]string{"go.mod": "go 1.27.3\n"})
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, ".airlock.toml")); err != nil {
		t.Skipf("symlinks: %v", err)
	}
	for _, args := range [][]string{{dir}, {dir, "--force"}} {
		code, _, errOut := runInit(t, args...)
		if code != 1 || !strings.Contains(errOut, "not a regular file") {
			t.Errorf("%v: exit %d, stderr %q; want a refusal", args, code, errOut)
		}
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep\n" {
		t.Errorf("the symlink target was written: %q", got)
	}
}

func TestInitBadPath(t *testing.T) {
	code, _, _ := runInit(t, filepath.Join(t.TempDir(), "missing"))
	if code != 2 {
		t.Errorf("missing path: exit %d, want 2", code)
	}
}

// hostileRune reports whether a terminal would act on r. The newline and the tab
// are the only ones init prints by itself.
func hostileRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, r == 0x2028, r == 0x2029:
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// TestInitHostileRepository runs init on repositories whose names and values
// carry escape sequences, C1 controls, bidi overrides and line breaks. Text and
// --json output must hold no such rune and no line that a repository value
// starts by itself. What is still safe to show must still show.
func TestInitHostileRepository(t *testing.T) {
	compose := func(image string) string { return "services:\n  db:\n    image: " + image + "\n" }
	cases := []struct {
		name    string
		files   map[string]string
		want    []string // substrings that must appear, in text and in JSON
		noStart []string // no output line may start with one of these
	}{
		{
			name:  "OSC 52 clipboard write and CSI clear in an image",
			files: map[string]string{"compose.yaml": compose("postgres\x1b]52;c;ZWNobyBoaQ==\x07\x1b[2J")},
		},
		{
			name:  "C1 CSI U+009B in an image",
			files: map[string]string{"compose.yaml": compose("postgres\u009b2J")},
		},
		{
			name:  "10 KB image value",
			files: map[string]string{"compose.yaml": compose(strings.Repeat("a", 10000) + "postgres")},
		},
		{
			name:  "bidi override in a folder that holds a database",
			files: map[string]string{"db\u202egnp/compose.yaml": compose("postgres:16")},
			want:  []string{"postgres:16 in db"},
		},
		{
			name:  "escape sequence in a folder that holds a database",
			files: map[string]string{"\x1b[2Jdb/compose.yaml": compose("mariadb:11")},
			want:  []string{"mariadb:11 in db/compose.yaml"},
		},
		{
			name:  "U+2028 in a folder that holds a database",
			files: map[string]string{"a\u2028b/compose.yaml": compose("redis:7")},
			want:  []string{"redis:7 in ab/compose.yaml"},
		},
		{
			name: "newline in a secret-looking file name",
			files: map[string]string{
				"evil\nkey.pem":   "x",
				"good.pem":        "x",
				"carriage\rr.pem": "x",
			},
			want:    []string{"good.pem", "2 file(s) with unusual names skipped"},
			noStart: []string{"key.pem", "r.pem", "evil"},
		},
		{
			name:  "bidi override in a secret-looking file name",
			files: map[string]string{"\u202eexe.pem": "x"},
			want:  []string{"1 file(s) with unusual names skipped"},
		},
		{
			name:  "escape sequence in engines.node",
			files: map[string]string{"package.json": `{"engines":{"node":">=20 <\u001b]52;c;QQ==\u0007"}}`},
		},
		{
			name:  "control character in requires-python",
			files: map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.11\u001b[2J\"\n"},
		},
	}
	for _, tc := range cases {
		for _, args := range [][]string{nil, {"--json"}} {
			label := tc.name
			if args != nil {
				label += " (json)"
			}
			t.Run(label, func(t *testing.T) {
				dir := writeProject(t, tc.files)
				code, out, errOut := runInit(t, append([]string{dir}, args...)...)
				if code != 0 {
					t.Fatalf("exit %d, stderr %q", code, errOut)
				}
				for _, stream := range []string{out, errOut} {
					for _, r := range stream {
						if hostileRune(r) {
							t.Fatalf("output has rune %U: %q", r, stream)
						}
					}
				}
				if args != nil {
					var rep map[string]any
					if err := json.Unmarshal([]byte(out), &rep); err != nil {
						t.Fatalf("invalid JSON: %v\n%s", err, out)
					}
				}
				for _, w := range tc.want {
					if !strings.Contains(out, w) {
						t.Errorf("output lacks %q:\n%s", w, out)
					}
				}
				for _, line := range strings.Split(out, "\n") {
					trimmed := strings.TrimLeft(line, " \t")
					for _, s := range tc.noStart {
						if strings.HasPrefix(trimmed, s) {
							t.Errorf("a line starts with %q from the repository:\n%s", s, out)
						}
					}
				}
				if len(out) > 4096 {
					t.Errorf("output is %d bytes", len(out))
				}
			})
		}
	}
}

// A MediaWiki checkout gets the per-project file printed, and nothing is written
// for it: .airlock.toml does not carry the sandbox or runner lines.
func TestInitPrintsMediaWikiRunnerFileOnly(t *testing.T) {
	dir := writeProject(t, map[string]string{"composer.json": `{"name":"mediawiki/core"}`})
	code, out, errb := runInit(t, dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	for _, want := range []string{
		"match = [",
		`worktree_link = ["LocalSettings.php", "vendor", "extensions/*", "skins/*"]`,
		"# [[runner]]",
		"# container = ",
		"(not written)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	written, err := os.ReadFile(filepath.Join(dir, ".airlock.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "worktree_link") || strings.Contains(string(written), "runner") {
		t.Errorf(".airlock.toml carries the per-project file:\n%s", written)
	}
}

func TestInitSnippetQuotesAndCleans(t *testing.T) {
	if got := tomlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("tomlString = %s", got)
	}
	// A path with control characters is not rewritten: the user types it.
	s := mediaWikiSnippet("/srv/a\x1b[31mb\"c")
	if strings.ContainsRune(cleanBlock(s), 0x1b) || !strings.Contains(cleanBlock(s), `match = ["<edit by hand>"]`) {
		t.Errorf("control characters: %q", s)
	}
	// Glob characters are escaped, and a backslash too, then TOML-quoted.
	if got := matchLine(`/srv/a*b?c[d]\e"f`); got != `match = ["/srv/a\\*b\\?c\\[d]\\\\e\"f"]  # glob characters in the path are escaped with a backslash` {
		t.Errorf("matchLine = %s", got)
	}
	if got := matchLine("/srv/plain"); got != `match = ["/srv/plain"]` {
		t.Errorf("plain path: %s", got)
	}
}
