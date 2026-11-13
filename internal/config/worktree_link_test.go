package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeLinkPatterns(t *testing.T) {
	valid := []string{
		"LocalSettings.php", "vendor", "extensions/*", "skins/*", ".env", "extensions/Foo",
		"*.php", ".env*", "config/local.json", "vendor/", "a/./b",
	}
	for _, p := range valid {
		if err := validLinkPattern(p, ".worktrees"); err != nil {
			t.Errorf("%q must be valid: %v", p, err)
		}
	}
	invalid := []string{
		"", "   ", "/etc/passwd", "~/notes", "~", "../outside", "a/../../b", "vendor/..", "..",
		".", "./", ".git", ".git/config", ".git/hooks", "sub/.git",
		".worktrees", ".worktrees/*", "x/.worktrees/y", "[", "a/[b", "a\x00b", "a\nb", "a\tb", `a\b`,
	}
	for _, p := range invalid {
		if err := validLinkPattern(p, ".worktrees"); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}

// The configured worktrees folder is refused too, whatever its name, and the
// check uses the value of the same configuration.
func TestWorktreeLinkRefusesTheConfiguredWorktreesFolder(t *testing.T) {
	for _, p := range []string{"wt", "wt/x", "a/wt", "wt/sub/y"} {
		if err := validLinkPattern(p, "wt/sub"); err == nil {
			t.Errorf("%q names the worktrees folder wt and must be refused", p)
		}
	}
	if err := validLinkPattern("vendor", "wt/sub"); err != nil {
		t.Errorf("vendor is fine: %v", err)
	}
	r, err := load(t, "[sandbox]\nworktrees_dir = \"wt\"\nworktree_link = [\"wt/x\"]\n", "", nil)
	if err == nil {
		t.Errorf("a pattern under the configured worktrees folder must fail validation: %+v", r.Config.Sandbox)
	}
}

func TestWorktreeLinkIsValidatedInTheConfig(t *testing.T) {
	r, err := load(t, "[sandbox]\nworktree_link = [\"LocalSettings.php\", \"vendor\"]\n", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Sandbox.WorktreeLink; len(got) != 2 || got[1] != "vendor" {
		t.Errorf("worktree_link = %v", got)
	}
	_, err = load(t, "[sandbox]\nworktree_link = [\"../escape\"]\n", "", nil)
	if err == nil || !strings.Contains(err.Error(), "sandbox.worktree_link") {
		t.Errorf("an invalid pattern must fail the user config, naming the key: %v", err)
	}
}

// worktree_link is not a key a project's .airlock.toml may set: the file is
// untrusted, and the link is a host path decision. The key is ignored and named.
func TestWorktreeLinkInDotAirlockTomlIsIgnoredAndReported(t *testing.T) {
	if projectAllowed[WorktreeLinkName] {
		t.Fatalf("%s must not be in projectAllowed", WorktreeLinkName)
	}
	r, err := load(t, "", "[sandbox]\nworktree_link = [\"vendor\"]\nui = \"shell\"\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Sandbox.WorktreeLink) != 0 {
		t.Errorf("a project may not set worktree_link: %v", r.Config.Sandbox.WorktreeLink)
	}
	if r.Config.Sandbox.UI != "shell" {
		t.Errorf("the allowed key of the same file must still apply: ui %q", r.Config.Sandbox.UI)
	}
	var reported bool
	for _, w := range r.Warnings {
		if strings.Contains(w, "sandbox.worktree_link") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the ignored key must be reported: %v", r.Warnings)
	}
}

// A project settings file is trusted, so the same key is accepted there.
func TestWorktreeLinkInAProjectSettingsFileApplies(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	write(t, filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "mw.toml"),
		"match = [\""+root+"\"]\n[sandbox]\nworktree_link = [\"LocalSettings.php\", \"vendor\"]\n")
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.Sandbox.WorktreeLink; len(got) != 2 {
		t.Errorf("worktree_link from a project settings file = %v", got)
	}
}
