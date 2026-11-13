package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
