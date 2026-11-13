package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/config"
)

// configSandbox points HOME and the config locations at a temp dir and makes a
// project folder (a bare .git entry is enough for airlock to call it one), so the
// test runs in the working directory it names and never reaches the real config.
func configSandbox(t *testing.T) (home, project string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AIRLOCK_CONFIG", "")
	project = filepath.Join(home, "Sites", "mw-core")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	return home, project
}

func runConfig(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(append([]string{"config"}, argv...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestConfigProjectPathInitAndRefusal(t *testing.T) {
	home, _ := configSandbox(t)
	want := filepath.Join(home, ".config", "airlock", "projects", "mw-core.toml")

	code, out, _ := runConfig(t, "path", "--project")
	if code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("config path --project: %d %q, want %s", code, out, want)
	}
	if _, err := os.Stat(want); err == nil {
		t.Fatal("path must not create the file")
	}

	if code, _, errs := runConfig(t, "init", "--project"); code != 0 {
		t.Fatalf("init --project: %d %s", code, errs)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `match = ["~/Sites/mw-core"]`) {
		t.Errorf("skeleton must collapse the home folder in match:\n%s", data)
	}
	if code, _, errs := runConfig(t, "init", "--project"); code != 1 || !strings.Contains(errs, "already exists") {
		t.Errorf("a second init must refuse without --force: %d %s", code, errs)
	}

	// The skeleton applies to this project: config show reports the layer.
	code, _, errs := runConfig(t, "show")
	if code != 0 {
		t.Fatalf("config show: %d %s", code, errs)
	}
	if !strings.Contains(errs, "project settings "+want) {
		t.Errorf("config show must list the applied project file:\n%s", errs)
	}
}

func TestConfigProjectEditCreatesAndOpensWithTheEditor(t *testing.T) {
	home, _ := configSandbox(t)
	t.Setenv("VISUAL", "true") // the editor just exits: the file is what matters
	t.Setenv("EDITOR", "")
	want := filepath.Join(home, ".config", "airlock", "projects", "mw-core.toml")
	if code, _, errs := runConfig(t, "edit", "--project"); code != 0 {
		t.Fatalf("edit --project: %d %s", code, errs)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("edit --project must create the file: %v", err)
	}
}

func TestConfigProjectNameGetsAHashWhenTakenByAnotherProject(t *testing.T) {
	home, _ := configSandbox(t)
	dir := filepath.Join(home, ".config", "airlock", "projects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file named mw-core.toml that belongs to another checkout of the same name.
	other := "match = [\"~/work/other/mw-core\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "mw-core.toml"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runConfig(t, "path", "--project")
	if code != 0 {
		t.Fatalf("config path --project: %d %s", code, errs)
	}
	if filepath.Base(strings.TrimSpace(out)) == "mw-core.toml" || !strings.HasPrefix(filepath.Base(strings.TrimSpace(out)), "mw-core-") {
		t.Errorf("another project's name must not be taken over: %q", out)
	}
	if code, _, errs := runConfig(t, "init", "--project"); code != 0 {
		t.Fatalf("init --project: %d %s", code, errs)
	}
	other2, err := os.ReadFile(filepath.Join(dir, "mw-core.toml"))
	if err != nil || string(other2) != other {
		t.Errorf("the other project's file must be left as it was: %v %q", err, other2)
	}
	chosen, exists, err := config.ProjectFileChoice(dir, filepath.Join(home, "Sites", "mw-core"))
	if err != nil || !exists || chosen != strings.TrimSpace(out) {
		t.Errorf("the new file must be the one chosen now: %s %v %v", chosen, exists, err)
	}
}

func TestConfigProjectRejectsOtherSubcommands(t *testing.T) {
	configSandbox(t)
	if code, _, _ := runConfig(t, "show", "--project"); code == 0 {
		t.Error("show --project is not a command: it must fail")
	}
}

// A worktree gets its main checkout's settings: the file names the main checkout.
func TestConfigShowInAWorktreeUsesTheMainCheckoutsSettings(t *testing.T) {
	main := linkRepo(t, `[]`)
	projects := filepath.Join(filepath.Dir(os.Getenv("AIRLOCK_CONFIG")), "projects")
	file := filepath.Join(projects, "core.toml")
	mustWrite(t, file, "match = [\""+main+"\"]\n[sandbox]\nui = \"shell\"\n")
	// An isolated worktree is the case where its own folder is not the project root.
	gitIn(t, main, "worktree", "add", "-q", "-b", "iso", filepath.Join(main, ".worktrees", ".isolated", "iso"))
	t.Chdir(filepath.Join(main, ".worktrees", ".isolated", "iso"))
	code, _, errs := runConfig(t, "show")
	if code != 0 || !strings.Contains(errs, "project settings "+file) {
		t.Errorf("config show in a worktree must list the main checkout's settings: %d\n%s", code, errs)
	}
}
