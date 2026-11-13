package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

// match: a string and a list are both accepted, and a bad type names the file.
func TestMatchStringOrList(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	pdir := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName)
	write(t, filepath.Join(pdir, "a-string.toml"), "match = \""+root+"\"\n[sandbox]\nui = \"shell\"\n")
	write(t, filepath.Join(pdir, "b-list.toml"), "match = [\"/elsewhere\", \""+root+"\"]\n[sandbox]\nmemory = \"1g\"\n")
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ProjectFiles) != 2 || res.Config.Sandbox.UI != "shell" || res.Config.Sandbox.Memory != "1g" {
		t.Errorf("both forms must apply: %v ui %q memory %q", res.ProjectFiles, res.Config.Sandbox.UI, res.Config.Sandbox.Memory)
	}
}

func TestMatchBadTypeNamesTheFile(t *testing.T) {
	for name, match := range map[string]string{
		"number":         "match = 5\n",
		"list of number": "match = [1, 2]\n",
		"empty list":     "match = []\n",
		"table":          "match = { a = 1 }\n",
	} {
		t.Run(name, func(t *testing.T) {
			userPath, root := projectFixture(t, "", nil)
			path := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "bad.toml")
			write(t, path, match+"[sandbox]\nui = \"shell\"\n")
			_, err := loadProject(t, userPath, root)
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("a bad match must stop airlock and name the file: %v", err)
			}
		})
	}
}

// A broken file whose match is readable and names other projects is skipped with
// a warning, and the rest of the configuration still loads.
func TestBrokenFileOfAnotherProjectIsSkipped(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	pdir := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName)
	broken := filepath.Join(pdir, "other.toml")
	write(t, broken, "match = [\"/somewhere/else\"]\n[sandbox]\nbogus = 1\n")
	write(t, filepath.Join(pdir, "mine.toml"), "match = [\""+root+"\"]\n[sandbox]\nui = \"shell\"\n")
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatalf("a broken file that names another project must not stop airlock: %v", err)
	}
	if res.Config.Sandbox.UI != "shell" || len(res.ProjectFiles) != 1 {
		t.Errorf("the good file must still apply: ui %q, %v", res.Config.Sandbox.UI, res.ProjectFiles)
	}
	var named bool
	for _, w := range res.Warnings {
		if strings.Contains(w, broken) {
			named = true
		}
	}
	if !named {
		t.Errorf("the skipped file must be named in a warning: %v", res.Warnings)
	}
}

// A broken file whose match names this project may apply here: airlock stops.
func TestBrokenFileThatNamesThisProjectStops(t *testing.T) {
	cases := map[string]string{
		"unknown key":         "match = [\"ROOT\"]\n[sandbox]\nbogus = 1\n",
		"syntax error":        "match = [\"ROOT\"]\n[sandbox\nui = \"shell\"\n",
		"wrong value type":    "match = [\"ROOT\"]\n[sandbox]\ncpus = \"x\"\n",
		"match among a list":  "match = [\"/x\", \"ROOT\"]\n[sandbox]\nbogus = 1\n",
		"match of a bad type": "match = 7\n[sandbox]\nbogus = 1\n",
		"relative and broken": "match = [\"relative\"]\n[sandbox]\nbogus = 1\n",
		"no match and broken": "[sandbox]\nbogus = 1\n",
		"syntax, no match":    "[sandbox\n",
		"syntax, bad pattern": "match = [\"/work/[\"]\n[sandbox\n",
		"mount wrong and bad": "match = [\"ROOT\"]\n[[mount]]\nname = \"n\"\nmode = \"zz\"\n[bogus]\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			userPath, root := projectFixture(t, "", nil)
			path := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "x.toml")
			write(t, path, strings.ReplaceAll(content, "ROOT", root))
			if _, err := loadProject(t, userPath, root); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("it may apply here, so airlock must stop and name the file: %v", err)
			}
		})
	}
}

// ProjectFileChoice follows the same rule: a broken file of another project does
// not take the name, and one that may be this project's stops the choice.
func TestProjectFileChoiceWithBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, ProjectFilesDirName)
	root := filepath.Join(dir, "work", "mw-core")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(projects, "mw-core.toml"), "match = [\"/elsewhere/mw-core\"]\n[sandbox]\nbogus = 1\n")
	p, exists, err := ProjectFileChoice(projects, root)
	if err != nil || exists || !strings.HasPrefix(filepath.Base(p), "mw-core-") {
		t.Fatalf("a broken file of another project must leave the name to the hash: %s %v %v", p, exists, err)
	}

	write(t, filepath.Join(projects, "mw-core.toml"), "match = [\""+root+"\"]\n[sandbox\n")
	if _, _, err := ProjectFileChoice(projects, root); err == nil || !strings.Contains(err.Error(), "mw-core.toml") {
		t.Fatalf("a broken file that may be this project's must stop the choice naming it: %v", err)
	}
}

// Trusted settings must not be reachable through a link that stow-style setups
// make: the configuration folder is a symlink into a dotfiles checkout.
func TestStowLayoutConfigFolderIsRefusedThroughTheSymlink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	real := filepath.Join(base, "dotfiles", "airlock")
	if err := os.MkdirAll(filepath.Join(real, ProjectFilesDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "xdg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "xdg", "airlock")); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(base, "xdg", "airlock", "config.toml")
	projects, ok := ProjectsDirFor(user, noEnv)
	if !ok {
		t.Fatal("config.toml has a projects folder")
	}
	folders := ConfigFolders(user, projects)
	refused := []string{
		real,
		filepath.Join(real, ProjectFilesDirName),
		filepath.Join(base, "xdg", "airlock", ProjectFilesDirName),
		filepath.Join(base, "xdg", "airlock", "config.toml"),
		filepath.Join(base, "dotfiles"), // holds the folder
		base,                            // holds everything
	}
	for _, p := range refused {
		if err := CheckFolderReach(p, "mount", folders); err == nil {
			t.Errorf("%s must be refused: it reaches the configuration folder", p)
		}
	}
	if err := CheckFolderReach(filepath.Join(base, "Sites", "mw-core"), "project root", folders); err != nil {
		t.Errorf("a legitimate project must not be refused: %v", err)
	}
}

// The case rule is the file system's: folded on macOS and Windows, exact elsewhere.
// The parameter lets both be tested on any host.
func TestReachesFoldsCaseOnlyWhereTheFileSystemDoes(t *testing.T) {
	if !reachesFold("/Users/x/Config/airlock", "/users/x/config", true) {
		t.Error("with case folding, a path in another case reaches the folder")
	}
	if reachesFold("/Users/x/Config/airlock", "/users/x/config", false) {
		t.Error("without case folding, another case is another folder")
	}
	if !reachesFold("/a/B/c", "/a/b", true) {
		t.Error("with case folding, a folder inside another folder in another case is found")
	}
	if reachesFold("/a/bc", "/a/b", false) {
		t.Error("a sibling with a longer name is not inside the folder")
	}
}

// AIRLOCK_CONFIG may name a file anywhere: its folder is protected then, and the
// check runs in Load against the folders of this run.
func TestAirlockConfigElsewhereProtectsItsFolder(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "elsewhere", "mine.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, cfg, "[[mount]]\nname = \"m\"\nhost = \""+filepath.Join(dir, "elsewhere")+"\"\nmode = \"ro\"\n")
	_, err = Load(LoadOptions{UserConfigPath: cfg, Getenv: noEnv})
	if err == nil || !strings.Contains(err.Error(), "reaches") {
		t.Fatalf("a mount of the folder of the config in use must be refused: %v", err)
	}
	if _, ok := ProjectsDirFor(cfg, noEnv); ok {
		t.Error("a config file that is not config.toml has no projects folder by default")
	}
}

func TestProjectsDirFor(t *testing.T) {
	if d, ok := ProjectsDirFor("/x/airlock/config.toml", noEnv); !ok || d != "/x/airlock/projects" {
		t.Errorf("config.toml: %q %v", d, ok)
	}
	if _, ok := ProjectsDirFor("/x/mine.toml", noEnv); ok {
		t.Error("another name has no folder")
	}
	env := func(k string) string {
		if k == ProjectsDirEnv {
			return "/pf"
		}
		return ""
	}
	if d, ok := ProjectsDirFor("/x/mine.toml", env); !ok || d != "/pf" {
		t.Errorf("AIRLOCK_PROJECTS_DIR names the folder: %q %v", d, ok)
	}
}

// Under AIRLOCK_PROJECTS_DIR a config file of any name has its settings files.
func TestProjectsDirEnvIsUsedForAnyConfigName(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "work", "mw-core")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(dir, "pf")
	if err := os.MkdirAll(pf, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(pf, "p.toml"), "match = [\""+root+"\"]\n[sandbox]\nui = \"shell\"\n")
	env := func(k string) string {
		if k == ProjectsDirEnv {
			return pf
		}
		return ""
	}
	res, err := Load(LoadOptions{UserConfigPath: filepath.Join(dir, "mine.toml"), ProjectDir: root, Getenv: env})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "shell" || len(res.ProjectFiles) != 1 {
		t.Errorf("the settings folder from the environment must apply: %v", res.ProjectFiles)
	}
}
