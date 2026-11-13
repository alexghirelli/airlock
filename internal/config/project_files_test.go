package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// projectFixture makes a user config, a projects folder next to it holding files,
// and a project root. It returns the user config path and the project root.
func projectFixture(t *testing.T, user string, files map[string]string) (userPath, root string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	userPath = filepath.Join(dir, "config.toml")
	if user != "" {
		write(t, userPath, user)
	}
	pdir := filepath.Join(dir, ProjectFilesDirName)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		write(t, filepath.Join(pdir, name), content)
	}
	root = filepath.Join(dir, "work", "mw-core")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return userPath, root
}

func loadProject(t *testing.T, userPath, root string) (*Result, error) {
	t.Helper()
	return Load(LoadOptions{UserConfigPath: userPath, ProjectDir: root, Getenv: func(string) string { return "" }})
}

func TestProjectFileMergesLikeTheUserConfig(t *testing.T) {
	user := "[sandbox]\nmemory = \"8g\"\ncpus = 4\n" +
		"[env]\nKEEP = { value = \"1\" }\nOVER = { value = \"old\" }\n" +
		"[[mount]]\nname = \"old\"\nhost = \"/srv/notes\"\nmode = \"ro\"\n"
	userPath, root := projectFixture(t, user, nil)
	project := "match = [\"~/no-such-folder\", \"" + root + "\"]\n" +
		"[sandbox]\ncpus = 2\n" +
		"[env]\nOVER = { value = \"new\" }\nADDED = { value = \"2\" }\n" +
		"[[mount]]\nname = \"new\"\nhost = \"/srv/notes\"\nmode = \"rw\"\n"
	write(t, filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "a.toml"), project)

	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.Sandbox.Memory != "8g" {
		t.Errorf("a key the project file does not set must keep the user's value, got %q", c.Sandbox.Memory)
	}
	if c.Sandbox.CPUs != 2 {
		t.Errorf("scalar: project value must win, got %v", c.Sandbox.CPUs)
	}
	// Same rule as the user config: a list of tables is replaced, not appended.
	if len(c.Mounts) != 1 || c.Mounts[0].Name != "new" {
		t.Errorf("[[mount]] must be replaced like the user layer does, got %+v", c.Mounts)
	}
	// Same rule as the user config: [env] merges key by key.
	wantEnv := map[string]Env{"KEEP": {Value: "1"}, "OVER": {Value: "new"}, "ADDED": {Value: "2"}}
	if !reflect.DeepEqual(c.Env, wantEnv) {
		t.Errorf("[env] merge: got %+v want %+v", c.Env, wantEnv)
	}
	if !reflect.DeepEqual(res.ProjectFiles, []string{filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "a.toml")}) {
		t.Errorf("ProjectFiles = %v", res.ProjectFiles)
	}
}

func TestProjectFilesApplyInNameOrderAndTheLaterWins(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	pdir := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName)
	m := "match = [\"" + root + "\"]\n"
	write(t, filepath.Join(pdir, "b.toml"), m+"[sandbox]\nui = \"agent\"\n")
	write(t, filepath.Join(pdir, "a.toml"), m+"[sandbox]\nui = \"shell\"\nmemory = \"2g\"\n")
	write(t, filepath.Join(pdir, "c.toml"), "match = [\"/elsewhere\"]\n[sandbox]\nui = \"herdr\"\n")

	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "agent" {
		t.Errorf("b.toml comes after a.toml and must win, got ui %q", res.Config.Sandbox.UI)
	}
	if res.Config.Sandbox.Memory != "2g" {
		t.Errorf("a.toml's memory must apply, got %q", res.Config.Sandbox.Memory)
	}
	if len(res.ProjectFiles) != 2 || filepath.Base(res.ProjectFiles[0]) != "a.toml" || filepath.Base(res.ProjectFiles[1]) != "b.toml" {
		t.Errorf("ProjectFiles = %v, want a.toml and b.toml only", res.ProjectFiles)
	}
}

func TestProjectDotAirlockTomlStillWinsOverProjectFiles(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	write(t, filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "p.toml"),
		"match = [\""+root+"\"]\n[sandbox]\nui = \"shell\"\n")
	write(t, filepath.Join(root, ProjectConfigName), "[sandbox]\nui = \"agent\"\n")
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "agent" {
		t.Errorf(".airlock.toml is the layer above project files; ui = %q", res.Config.Sandbox.UI)
	}
}

func TestProjectFileMatchForms(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	pdir := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName)
	parent := filepath.Dir(root)
	// A glob on the parent matches the project; a glob one level up does not.
	write(t, filepath.Join(pdir, "glob.toml"), "match = [\""+parent+"/mw-*\"]\n[sandbox]\nui = \"shell\"\n")
	write(t, filepath.Join(pdir, "deep.toml"), "match = [\""+filepath.Dir(parent)+"/*\"]\n[sandbox]\nmemory = \"1g\"\n")
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "shell" {
		t.Errorf("glob must match the project: ui = %q", res.Config.Sandbox.UI)
	}
	if res.Config.Sandbox.Memory == "1g" {
		t.Errorf("* must not match a path deeper than the folder it names")
	}
	if len(res.ProjectFiles) != 1 {
		t.Errorf("ProjectFiles = %v", res.ProjectFiles)
	}
}

func TestProjectFileTildeExpandsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath, _ := projectFixture(t, "", nil)
	root := filepath.Join(home, "Sites", "mw-core")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(homeReal, "Sites", "mw-core")
	write(t, filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "t.toml"), "match = [\"~/Sites/mw-core\"]\n[sandbox]\nui = \"shell\"\n")
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "shell" {
		t.Errorf("~ must expand to the home folder, ui = %q", res.Config.Sandbox.UI)
	}
}

func TestProjectFileWithoutAProjectIsNotRead(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	write(t, filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "p.toml"), "match = [\""+root+"\"]\n[sandbox]\nui = \"shell\"\n")
	res, err := Load(LoadOptions{UserConfigPath: userPath, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "herdr" || len(res.ProjectFiles) != 0 {
		t.Errorf("no project directory: project files must not apply (ui %q, %v)", res.Config.Sandbox.UI, res.ProjectFiles)
	}
}

func TestProjectFileErrorsNameTheFile(t *testing.T) {
	cases := map[string]string{
		"missing match":    "[sandbox]\nui = \"shell\"\n",
		"empty match":      "match = []\n",
		"blank match":      "match = [\"  \"]\n",
		"relative match":   "match = [\"work/mw-core\"]\n",
		"bad glob":         "match = [\"/work/[\"]\n",
		"unknown key":      "match = [\"ROOT\"]\n[sandbox]\nbogus = 1\n",
		"malformed toml":   "match = [\"ROOT\"\n",
		"wrong type":       "match = [\"ROOT\"]\n[sandbox]\ncpus = \"four\"\n",
		"invalid merged":   "match = [\"ROOT\"]\n[sandbox]\nidle_stop = \"1s\"\n",
		"unknown section":  "match = [\"ROOT\"]\n[bogus]\nx = 1\n",
		"mount wrong mode": "match = [\"ROOT\"]\n[[mount]]\nname = \"n\"\nhost = \"/tmp/x\"\nmode = \"wx\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			userPath, root := projectFixture(t, "", nil)
			path := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "broken.toml")
			write(t, path, strings.ReplaceAll(content, "ROOT", root))
			_, err := loadProject(t, userPath, root)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the error must name the file %s: %v", path, err)
			}
		})
	}
}

// The sandbox must never see the folder the trusted settings live in: a mount of
// it, of a folder that holds it, or of a file in it is refused.
func TestMountOfTheConfigFolderIsRefused(t *testing.T) {
	cfgRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	airlock := filepath.Join(cfgRoot, "airlock")
	refused := []string{airlock, filepath.Join(airlock, "projects"), filepath.Join(airlock, "config.toml"), cfgRoot}
	for _, host := range refused {
		c := Defaults()
		c.Mounts = []Mount{{Name: "m", Host: host, Mode: "ro"}}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "reaches") {
			t.Errorf("mount of %s must be refused: %v", host, err)
		}
	}
	c := Defaults()
	c.Mounts = []Mount{{Name: "m", Host: filepath.Join(filepath.Dir(cfgRoot), "unrelated"), Mode: "ro"}}
	if err := c.Validate(); err != nil {
		t.Errorf("an unrelated folder beside the config folder may be mounted: %v", err)
	}
}

func TestProjectFileThatMatchesNothingIsIgnoredEvenWithOtherSettings(t *testing.T) {
	userPath, root := projectFixture(t, "", map[string]string{
		"other.toml": "match = [\"/somewhere/else\"]\n[sandbox]\nui = \"shell\"\n",
	})
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "herdr" || len(res.ProjectFiles) != 0 {
		t.Errorf("a file that matches nothing must be ignored: ui %q, %v", res.Config.Sandbox.UI, res.ProjectFiles)
	}
}

func TestProjectFileSizeIsCapped(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	path := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "big.toml")
	big := "match = [\"" + root + "\"]\n# " + strings.Repeat("x", MaxProjectFileSize) + "\n"
	write(t, path, big)
	_, err := loadProject(t, userPath, root)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("an oversized file must be refused and named: %v", err)
	}
}

func TestProjectFileSymlinks(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	pdir := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName)
	outside := t.TempDir()
	target := filepath.Join(outside, "real.toml")
	write(t, target, "match = [\""+root+"\"]\n[sandbox]\nui = \"shell\"\n")
	// A link to a regular file the user owns is followed.
	if err := os.Symlink(target, filepath.Join(pdir, "linked.toml")); err != nil {
		t.Fatal(err)
	}
	res, err := loadProject(t, userPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Sandbox.UI != "shell" || len(res.ProjectFiles) != 1 {
		t.Errorf("a link to an owned regular file must apply: ui %q, %v", res.Config.Sandbox.UI, res.ProjectFiles)
	}
}

// A dangling link cannot be read, so its match is unknown: it stops airlock.
func TestDanglingProjectSymlinkStopsAirlock(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	link := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "dangling.toml")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing.toml"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProject(t, userPath, root); err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("a dangling link must stop airlock and name the file: %v", err)
	}
}

// A link to a folder is refused, and the refusal cannot be checked: it stops too.
func TestProjectSymlinkToAFolderStopsAirlock(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	link := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "folder.toml")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProject(t, userPath, root); err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("a link to a folder must stop airlock and name the file: %v", err)
	}
}

// A link that resolves inside the project is refused: the project's own files are
// not a place for trusted settings.
func TestProjectSymlinkIntoTheProjectStopsAirlock(t *testing.T) {
	userPath, root := projectFixture(t, "", nil)
	inside := filepath.Join(root, "settings.toml")
	write(t, inside, "match = [\""+root+"\"]\n[sandbox]\nui = \"shell\"\n")
	link := filepath.Join(filepath.Dir(userPath), ProjectFilesDirName, "inside.toml")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProject(t, userPath, root); err == nil || !strings.Contains(err.Error(), "inside the project") {
		t.Fatalf("a link into the project must be refused: %v", err)
	}
}

func TestProjectFilesDirMissingIsFine(t *testing.T) {
	dir := t.TempDir()
	res, err := loadProject(t, filepath.Join(dir, "config.toml"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ProjectFiles) != 0 {
		t.Errorf("ProjectFiles = %v", res.ProjectFiles)
	}
}

func TestProjectFileChoiceNamesAndCollisions(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, ProjectFilesDirName)
	root := filepath.Join(dir, "work", "mw-core")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	p, exists, err := ProjectFileChoice(projects, root)
	if err != nil || exists || filepath.Base(p) != "mw-core.toml" {
		t.Fatalf("fresh name: %s %v %v", p, exists, err)
	}
	write(t, p, mustTemplate(t, root))
	p2, exists, err := ProjectFileChoice(projects, root)
	if err != nil || !exists || p2 != p {
		t.Fatalf("the file that matches this root is chosen: %s %v %v", p2, exists, err)
	}
	// Another project with the same folder name takes the plain name.
	other := filepath.Join(dir, "elsewhere", "mw-core")
	p3, exists, err := ProjectFileChoice(projects, other)
	if err != nil || exists || filepath.Base(p3) == "mw-core.toml" || !strings.HasPrefix(filepath.Base(p3), "mw-core-") {
		t.Fatalf("a name taken by another project gets a hash: %s %v %v", p3, exists, err)
	}
	write(t, p3, mustTemplate(t, other))
	if p4, exists, err := ProjectFileChoice(projects, other); err != nil || !exists || p4 != p3 {
		t.Fatalf("second lookup must find its own file: %s %v %v", p4, exists, err)
	}
}

func mustTemplate(t *testing.T, root string) string {
	t.Helper()
	tpl, err := ProjectFileTemplate(root)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

// The skeleton's match must name exactly the root it was made for, even when the
// root holds glob characters or a backslash.
func TestProjectFileTemplateMatchesItsRoot(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{`we"ird`, "a[b]", "a*b", "a?b", `a\b`, "plain"} {
		root := filepath.Join(base, name, "mw-core")
		tpl, err := ProjectFileTemplate(root)
		if runtime.GOOS == "windows" {
			if name != "plain" && err == nil {
				t.Errorf("%s: on Windows a glob character cannot be escaped, so it must be refused", root)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		data := []byte(tpl)
		patterns, err := parseProjectFile(data)
		if err != nil {
			t.Fatalf("%s: the skeleton must parse: %v\n%s", root, err, data)
		}
		if !matchesRoot(patterns, root) {
			t.Errorf("skeleton match %v does not name %s", patterns, root)
		}
		if name != "plain" && matchesRoot(patterns, filepath.Join(base, "aXb", "mw-core")) {
			t.Errorf("%s: the escaped match must not be a wildcard", root)
		}
	}
}
