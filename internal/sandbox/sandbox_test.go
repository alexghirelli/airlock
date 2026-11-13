package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/config"
)

func mkRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSameNameProjectsGetDifferentContainers(t *testing.T) {
	base := realTemp(t)
	a := mkRepo(t, filepath.Join(base, "a", "app"))
	b := mkRepo(t, filepath.Join(base, "b", "app"))
	pa, err := Resolve(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Resolve(b)
	if err != nil {
		t.Fatal(err)
	}
	if pa.ContainerName() == pb.ContainerName() {
		t.Fatalf("collision: both %s", pa.ContainerName())
	}
	if !strings.HasPrefix(pa.ContainerName(), "airlock-app-") {
		t.Errorf("unexpected name %q", pa.ContainerName())
	}
	again, _ := Resolve(a)
	if again.ContainerName() != pa.ContainerName() {
		t.Error("name is not stable for the same path")
	}
}

func TestSlugSanitizes(t *testing.T) {
	got := slug("/x/My Project (v2)!")
	if !strings.HasPrefix(got, "my-project-v2-") {
		t.Errorf("slug = %q", got)
	}
	if s := slug("/x/日本語"); !strings.HasPrefix(s, "project-") {
		t.Errorf("non-ascii name = %q", s)
	}
	long := slug("/x/" + strings.Repeat("a", 80))
	if len(long) > 24+1+8 {
		t.Errorf("slug too long: %q", long)
	}
}

func TestResolveFindsGitRootFromSubdirectory(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	sub := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root {
		t.Errorf("root = %q, want %q", p.Root, root)
	}
}

// mkWorktree fakes `git worktree add <root>/.worktrees/<name>` and resolves it.
func mkWorktree(t *testing.T, root, name string) *Project {
	t.Helper()
	wt := filepath.Join(root, ".worktrees", name)
	admin := filepath.Join(root, ".git", "worktrees", name)
	for _, d := range []string{wt, admin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve(wt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveFromInsideAWorktreeGivesTheWorktreeItsOwnProject(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	main, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	wt := mkWorktree(t, root, "w1")
	if want := filepath.Join(root, ".worktrees", "w1"); wt.Root != want {
		t.Errorf("worktree resolved to %q, want %q", wt.Root, want)
	}
	if !wt.IsWorktree() || wt.Main != root || wt.RepoRoot() != root || wt.GitDir() != filepath.Join(root, ".git") {
		t.Errorf("worktree project = %+v", wt)
	}
	if main.IsWorktree() || main.RepoRoot() != root {
		t.Errorf("main project = %+v", main)
	}
	if wt.ContainerName() == main.ContainerName() || wt.ContainerName() == mkWorktree(t, root, "w2").ContainerName() {
		t.Error("worktrees must not share a container with the main checkout or each other")
	}
	if !strings.HasPrefix(wt.Slug, "repo-w1-") {
		t.Errorf("slug %q does not say which repository and worktree it is", wt.Slug)
	}
	if wt.FamilySlug() != main.Slug {
		t.Errorf("family slug %q, want the main repository's %q", wt.FamilySlug(), main.Slug)
	}
	// From a subdirectory of the worktree it is still that worktree.
	sub := filepath.Join(wt.Root, "src")
	os.MkdirAll(sub, 0o755)
	if p, err := Resolve(sub); err != nil || p.Root != wt.Root {
		t.Errorf("subdirectory of a worktree: %+v, %v", p, err)
	}
}

func TestSubmoduleIsNotTreatedAsAWorktree(t *testing.T) {
	dir := realTemp(t)
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644)
	p, err := Resolve(dir)
	if err != nil || p.IsWorktree() || p.Root != dir {
		t.Errorf("submodule: %+v, %v", p, err)
	}
}

func TestResolveRefusesHomeAndRoot(t *testing.T) {
	home := realTemp(t)
	t.Setenv("HOME", home)
	if _, err := Resolve(home); err == nil {
		t.Error("home directory accepted as a project")
	}
	if _, err := Resolve("/"); err == nil {
		t.Error("filesystem root accepted as a project")
	}
}

func TestContainerPathWindows(t *testing.T) {
	if got := containerPath(`C:\Users\me\proj`); got != "/c/Users/me/proj" {
		t.Errorf("got %q", got)
	}
	if got := containerPath("/home/me/proj"); got != "/home/me/proj" {
		t.Errorf("got %q", got)
	}
}

// testWorktreeConfig is what the stubbed `git config extensions.worktreeConfig` returns.
var testWorktreeConfig string

func testPlan(t *testing.T, userToml string, project *Project, env map[string]string) *Plan {
	t.Helper()
	dir := realTemp(t)
	cfgPath := filepath.Join(dir, "user.toml")
	if err := os.WriteFile(cfgPath, []byte(userToml), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{UserConfigPath: cfgPath, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(PlanInput{
		Project: project, Config: res, Image: "airlock:test",
		Host: Host{UID: 1234, GID: 567, Getenv: func(k string) string { return env[k] },
			GitConfig: func(_, k string) string {
				return map[string]string{"user.name": "Ada", "user.email": "ada@example.org", "extensions.worktreeConfig": testWorktreeConfig}[k]
			}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func newProject(t *testing.T) *Project {
	t.Helper()
	root := mkRepo(t, filepath.Join(realTemp(t), "demo"))
	p, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func findBind(p *Plan, target string) *backend.Bind {
	for i := range p.Spec.Binds {
		if p.Spec.Binds[i].Target == target {
			return &p.Spec.Binds[i]
		}
	}
	return nil
}

func TestPlanIsHardenedByDefault(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "", proj, nil)
	s := plan.Spec

	for _, bad := range []string{"SYS_ADMIN", "NET_RAW", "SYS_PTRACE", "SYS_MODULE", "DAC_READ_SEARCH"} {
		for _, c := range s.CapAdd {
			if c == bad {
				t.Errorf("capability %s must not be granted", bad)
			}
		}
	}
	for _, b := range s.Binds {
		if strings.Contains(b.Source, "docker.sock") {
			t.Errorf("docker socket mounted: %+v", b)
		}
	}
	if p := findBind(plan, proj.Path); p == nil || p.ReadOnly || p.Source != proj.Root {
		t.Errorf("project must be bind-mounted read-write at its own path, got %+v", p)
	}
	for _, name := range []string{"hooks", "config"} {
		b := findBind(plan, filepath.Join(proj.Path, ".git", name))
		if b == nil || !b.ReadOnly {
			t.Errorf(".git/%s must be mounted read-only, got %+v", name, b)
		}
	}
	envs := strings.Join(s.Env, "\n")
	for _, want := range []string{"AIRLOCK_NET_MODE=open-no-lan", "AIRLOCK_UID=1234", "AIRLOCK_GID=567", "MISE_TRUSTED_CONFIG_PATHS=" + proj.Path} {
		if !strings.Contains(envs, want) {
			t.Errorf("env missing %q:\n%s", want, envs)
		}
	}
	if s.Network != NetworkName || s.WorkDir != proj.Path || !s.Init {
		t.Errorf("unexpected spec: %+v", s)
	}
	if s.MemoryBytes != 8<<30 || s.NanoCPUs != 4e9 || s.Pids != 2048 {
		t.Errorf("resource limits: %d %d %d", s.MemoryBytes, s.NanoCPUs, s.Pids)
	}
	if len(s.Volumes) != 2 || s.Volumes[0].Name != "airlock-creds" {
		t.Errorf("volumes: %+v", s.Volumes)
	}
	if s.Runtime != "" || s.AutoRemove {
		t.Errorf("container backend must use the default runtime and persist: %+v", s)
	}
}

func TestPlanPerProjectCredentialsAndGvisorAndEphemeral(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "[credentials]\nscope=\"per-project\"\n[sandbox]\nbackend=\"gvisor\"\nlifetime=\"ephemeral\"\n", proj, nil)
	if got := plan.Spec.Volumes[0].Name; got != "airlock-creds-"+proj.Slug {
		t.Errorf("creds volume = %q", got)
	}
	if plan.Spec.Runtime != "runsc" || !plan.Spec.AutoRemove {
		t.Errorf("gvisor/ephemeral not applied: %+v", plan.Spec)
	}
}

func TestPlanMountsAndAgentsMD(t *testing.T) {
	proj := newProject(t)
	notes := realTemp(t)
	skills := realTemp(t)
	missing := filepath.Join(realTemp(t), "nope")
	toml := "[[mount]]\nname=\"notes\"\nhost=\"" + notes + "\"\nmode=\"rw\"\ndescription=\"My vault. Read README first.\"\n" +
		"[[mount]]\nname=\"skills\"\nhost=\"" + skills + "\"\nmode=\"ro\"\nskills=\"sub\"\n" +
		"[[mount]]\nname=\"gone\"\nhost=\"" + missing + "\"\nmode=\"ro\"\n"
	plan := testPlan(t, toml, proj, nil)

	if b := findBind(plan, "/mnt/notes"); b == nil || b.ReadOnly || b.Source != notes {
		t.Errorf("notes mount: %+v", b)
	}
	if b := findBind(plan, "/mnt/skills"); b == nil || !b.ReadOnly {
		t.Errorf("skills mount must be read-only: %+v", b)
	}
	if findBind(plan, "/mnt/gone") != nil {
		t.Error("missing host path must be skipped, not mounted")
	}
	warned := false
	for _, w := range plan.Warnings {
		if strings.Contains(w, `"gone"`) && strings.Contains(w, "does not exist") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning for the missing mount: %v", plan.Warnings)
	}

	var agents, manifest string
	for _, f := range plan.Files {
		switch f.Path {
		case "/etc/airlock/AGENTS.md":
			agents = string(f.Data)
		case "/etc/airlock/manifest.json":
			manifest = string(f.Data)
		}
	}
	for _, want := range []string{"/mnt/notes` (rw): My vault. Read README first.", "/mnt/skills` (ro)", "Pi skills are loaded from `/mnt/skills/sub`", proj.Path} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, agents)
		}
	}
	if strings.Contains(agents, "/mnt/gone") {
		t.Error("AGENTS.md lists a mount that was skipped")
	}
	var piArgs string
	for _, f := range plan.Files {
		if f.Path == "/etc/airlock/pi.args" {
			piArgs = string(f.Data)
		}
	}
	if piArgs != "--skill\n/etc/airlock/skills/concise\n--skill\n/mnt/skills/sub\n" {
		t.Errorf("pi.args = %q", piArgs)
	}
	if strings.Contains(manifest, "/mnt/skills") {
		t.Error("per-project skill paths must not go into the shared Pi settings overlay")
	}
	for _, want := range []string{`"name": "Ada"`, `"email": "ada@example.org"`} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %s", want)
		}
	}
}

func TestAgentsMDDescribesNetworkAndGitModes(t *testing.T) {
	proj := newProject(t)
	get := func(toml string, env map[string]string) string {
		for _, f := range testPlan(t, toml, proj, env).Files {
			if f.Path == "/etc/airlock/AGENTS.md" {
				return string(f.Data)
			}
		}
		return ""
	}
	if a := get("", nil); !strings.Contains(a, "blocked by a firewall") || !strings.Contains(a, "cannot push") ||
		!strings.Contains(a, "airlock wt") {
		t.Errorf("default AGENTS.md:\n%s", a)
	}
	if a := get("[network]\nmode=\"open\"\n", nil); !strings.Contains(a, "No network restrictions") {
		t.Errorf("open mode not described:\n%s", a)
	}
	push := get("[git]\nmode=\"push\"\ntoken_env=\"GH_TOKEN_X\"\n", map[string]string{"GH_TOKEN_X": "s3cret"})
	if !strings.Contains(push, "credential helper") || strings.Contains(push, "s3cret") {
		t.Errorf("push mode text wrong or leaks the token:\n%s", push)
	}
	if a := get("[sandbox]\nworktrees_dir=\"\"\n", nil); strings.Contains(a, "### Worktrees") {
		t.Error("worktree section must disappear when worktrees are disabled")
	}
}

func TestGitPushModeNeedsTokenAndKeepsItOutOfPolicyHash(t *testing.T) {
	proj := newProject(t)
	res, err := config.Load(config.LoadOptions{
		UserConfigPath: writeTemp(t, "[git]\nmode=\"push\"\ntoken_env=\"MY_TOKEN\"\n"),
		Getenv:         func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	in := PlanInput{Project: proj, Config: res, Image: "x", Host: Host{Getenv: func(string) string { return "" }}}
	if _, err := BuildPlan(in); err == nil || !strings.Contains(err.Error(), "MY_TOKEN") {
		t.Fatalf("want an error about the unset token variable, got %v", err)
	}
	in.Host.Getenv = func(string) string { return "tok-one" }
	p1, err := BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Host.Getenv = func(string) string { return "tok-two" }
	p2, _ := BuildPlan(in)
	if p1.Policy != p2.Policy {
		t.Error("rotating the token must not change the policy hash")
	}
	if !strings.Contains(strings.Join(p1.Spec.Env, "\n"), "AIRLOCK_GIT_TOKEN=tok-one") {
		t.Error("token not passed in push mode")
	}
	local := testPlan(t, "", proj, map[string]string{"MY_TOKEN": "tok"})
	if strings.Contains(strings.Join(local.Spec.Env, "\n"), "tok") {
		t.Error("token leaked in local mode")
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(realTemp(t), "c.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPolicyChangesWithNetworkMountsAndImage(t *testing.T) {
	proj := newProject(t)
	base := testPlan(t, "", proj, nil).Policy
	if base != testPlan(t, "", proj, nil).Policy {
		t.Fatal("policy hash is not deterministic")
	}
	for name, toml := range map[string]string{
		"network":  "[network]\nmode=\"open\"\n",
		"cidr":     "[network]\nallow_cidrs=[\"192.168.1.5/32\"]\n",
		"memory":   "[sandbox]\nmemory=\"1g\"\n",
		"mount":    "[[mount]]\nname=\"n\"\nhost=\"" + realTemp(t) + "\"\nmode=\"ro\"\n",
		"scope":    "[credentials]\nscope=\"per-project\"\n",
		"backend":  "[sandbox]\nbackend=\"gvisor\"\n",
		"lifetime": "[sandbox]\nlifetime=\"ephemeral\"\n",
	} {
		if testPlan(t, toml, proj, nil).Policy == base {
			t.Errorf("changing %s did not change the policy hash", name)
		}
	}
}

func TestPiArgsCarryDefaultModelNotSharedSettings(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "[pi]\ndefault_model=\"prov/some model\"\n", proj, nil)
	var args, manifest string
	for _, f := range plan.Files {
		switch f.Path {
		case "/etc/airlock/pi.args":
			args = string(f.Data)
		case "/etc/airlock/manifest.json":
			manifest = string(f.Data)
		}
	}
	if args != "--skill\n/etc/airlock/skills/concise\n--model\nprov/some model\n" {
		t.Errorf("pi.args = %q", args)
	}
	if strings.Contains(manifest, "defaultModel") || strings.Contains(manifest, "prov/some") {
		t.Errorf("default model must not be written to the shared settings overlay:\n%s", manifest)
	}
	if !strings.Contains(manifest, `"npmCommand"`) {
		t.Errorf("npmCommand missing from the settings overlay:\n%s", manifest)
	}
}

func TestUserEnvPassthrough(t *testing.T) {
	proj := newProject(t)
	toml := "[env]\nOPENCODE_API_KEY = { from_host_env = \"HOST_KEY\" }\nUNSET_ONE = { from_host_env = \"NOPE\" }\n"
	plan := testPlan(t, toml, proj, map[string]string{"HOST_KEY": "abc"})
	envs := strings.Join(plan.Spec.Env, "\n")
	if !strings.Contains(envs, "OPENCODE_API_KEY=abc") {
		t.Errorf("env not passed:\n%s", envs)
	}
	if strings.Contains(envs, "UNSET_ONE") {
		t.Error("unset host variable must be skipped")
	}
}

func TestMCPHeadersUseHostEnvReferences(t *testing.T) {
	proj := newProject(t)
	toml := "[[mcp]]\nname=\"docs\"\nurl=\"https://example.com/mcp\"\ndescription=\"Docs\"\nheaders={Authorization=\"DOCS_AUTH\"}\n"
	plan := testPlan(t, toml, proj, map[string]string{"DOCS_AUTH": "Bearer zzz"})
	var manifest string
	for _, f := range plan.Files {
		if f.Path == "/etc/airlock/manifest.json" {
			manifest = string(f.Data)
		}
	}
	if !strings.Contains(manifest, `"${DOCS_AUTH}"`) || strings.Contains(manifest, "zzz") {
		t.Errorf("manifest must reference the variable, never hold its value:\n%s", manifest)
	}
	if !strings.Contains(strings.Join(plan.Spec.Env, "\n"), "DOCS_AUTH=Bearer zzz") {
		t.Error("header variable not forwarded into the container environment")
	}
}

func TestModelsEnvFromConfig(t *testing.T) {
	proj := newProject(t)
	toml := "[pi]\ndefault_model=\"prov/base\"\n[team.models]\nlead=\"prov/strong\"\nscout=\"it's/cheap\"\n"
	var env string
	for _, f := range testPlan(t, toml, proj, nil).Files {
		if f.Path == "/etc/airlock/models.env" {
			env = string(f.Data)
		}
	}
	for _, want := range []string{"MODEL_LEAD='prov/strong'", "MODEL_REVIEWER='prov/base'", `MODEL_SCOUT='it'\''s/cheap'`, "THINK_SCRIBE='low'", "DEFAULT_ROLES='reviewer tester scout scribe'"} {
		if !strings.Contains(env, want) {
			t.Errorf("models.env missing %q:\n%s", want, env)
		}
	}
	// The generated file must be valid shell that yields the intended values,
	// including the awkward quote in the scout model.
	file := filepath.Join(t.TempDir(), "models.env")
	if err := os.WriteFile(file, []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-c", `source "$1"; printf '%s|%s|%s' "$MODEL_SCOUT" "$MODEL_LEAD" "$DEFAULT_ROLES"`, "bash", file).CombinedOutput()
	if err != nil {
		t.Fatalf("models.env is not valid shell: %v\n%s", err, out)
	}
	if want := "it's/cheap|prov/strong|reviewer tester scout scribe"; string(out) != want {
		t.Errorf("sourced values = %q, want %q", out, want)
	}
}

func TestPrepareHostCreatesAndExcludesWorktrees(t *testing.T) {
	proj := newProject(t)
	if _, err := PrepareHost(proj, ".worktrees"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(proj.Root, ".worktrees")); err != nil || !st.IsDir() {
		t.Fatalf("worktrees dir not created: %v", err)
	}
	read := func() string {
		b, _ := os.ReadFile(filepath.Join(proj.Root, ".git", "info", "exclude"))
		return string(b)
	}
	if got := read(); got != "/.worktrees/\n" {
		t.Errorf("exclude = %q", got)
	}
	// Idempotent, and respects existing content without a trailing newline.
	if _, err := PrepareHost(proj, ".worktrees"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(read(), "/.worktrees/") != 1 {
		t.Errorf("pattern duplicated: %q", read())
	}
	os.WriteFile(filepath.Join(proj.Root, ".git", "info", "exclude"), []byte("*.log"), 0o644)
	if _, err := PrepareHost(proj, ".worktrees"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "*.log\n/.worktrees/\n" {
		t.Errorf("exclude = %q", got)
	}
	// Disabled: nothing created.
	other := newProject(t)
	if _, err := PrepareHost(other, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other.Root, ".worktrees")); err == nil {
		t.Error("worktrees dir created although disabled")
	}
}

func TestPrepareHostWarnsOutsideGit(t *testing.T) {
	dir := realTemp(t)
	p, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	warns, err := PrepareHost(p, ".worktrees")
	if err != nil || len(warns) == 0 {
		t.Errorf("want a warning for a non-git project, got %v %v", warns, err)
	}
	plan := testPlan(t, "", p, nil)
	for _, b := range plan.Spec.Binds {
		if strings.HasSuffix(b.Target, "/.git/hooks") {
			t.Error("no .git protection mounts expected outside a repository")
		}
	}
}

func TestPlanDoesNotAlterTheSharedSpecLabels(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "", proj, nil)
	if plan.Spec.Labels[backend.LabelPolicy] != plan.Policy || plan.Spec.Labels[backend.LabelProject] != proj.Root {
		t.Errorf("labels: %+v", plan.Spec.Labels)
	}
}

func TestAirlockIgnoreMasksMatchingFiles(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	proj := newProject(t)
	for path, content := range map[string]string{
		".env": "SECRET=1", "app/.env.local": "x", "app/ok.txt": "fine", "certs/server.pem": "k",
		"secrets/a.txt": "s", "secrets/b/c.txt": "s",
	} {
		full := filepath.Join(proj.Root, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(content), 0o644)
	}
	os.WriteFile(filepath.Join(proj.Root, ".airlockignore"), []byte(".env*\n*.pem\nsecrets/\n"), 0o644)

	plan := testPlan(t, "", proj, nil)
	masked := map[string]bool{}
	for _, b := range plan.Spec.Binds {
		if strings.HasPrefix(b.Source, StateDir()) {
			if !b.ReadOnly {
				t.Errorf("mask %s must be read-only", b.Target)
			}
			masked[strings.TrimPrefix(b.Target, proj.Path+"/")] = true
		}
	}
	for _, want := range []string{".env", "app/.env.local", "certs/server.pem", "secrets"} {
		if !masked[want] {
			t.Errorf("%s not masked; masks: %v", want, masked)
		}
	}
	for _, not := range []string{"app/ok.txt", "secrets/a.txt", "secrets/b"} {
		if masked[not] {
			t.Errorf("%s must not be masked separately (parent dir covers it, or it is not secret)", not)
		}
	}
	// The mask sources are really empty.
	if st, err := os.Stat(filepath.Join(StateDir(), "mask", "empty-file")); err != nil || st.Size() != 0 {
		t.Errorf("empty mask file: %v %v", st, err)
	}
	// Masks do not influence the policy hash.
	os.WriteFile(filepath.Join(proj.Root, "new.pem"), []byte("k"), 0o644)
	if testPlan(t, "", proj, nil).Policy != plan.Policy {
		t.Error("adding a masked file changed the policy hash")
	}
}

func TestNoAirlockIgnoreMeansNoMasking(t *testing.T) {
	proj := newProject(t)
	os.WriteFile(filepath.Join(proj.Root, ".env"), []byte("x"), 0o644)
	for _, b := range testPlan(t, "", proj, nil).Spec.Binds {
		if strings.HasSuffix(b.Target, "/.env") {
			t.Error(".env masked without an .airlockignore")
		}
	}
}

func TestAllowlistModeFailsClearlyUntilImplemented(t *testing.T) {
	proj := newProject(t)
	res, err := config.Load(config.LoadOptions{UserConfigPath: writeTemp(t, "[network]\nmode=\"allowlist\"\n"), Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Config: res, Project: proj}
	if _, err := m.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("want a clear 'not implemented' error, got %v", err)
	}
}

func TestMCPCommandServer(t *testing.T) {
	proj := newProject(t)
	toml := "[[mcp]]\nname=\"tracker\"\ncommand=\"node\"\nargs=[\"/mnt/t/index.js\"]\nenv={USER_NAME=\"alice\"}\nenv_from_host={TOKEN=\"TRACKER_TOKEN\"}\n"
	plan := testPlan(t, toml, proj, map[string]string{"TRACKER_TOKEN": "s3cret"})
	var manifest string
	for _, f := range plan.Files {
		if f.Path == "/etc/airlock/manifest.json" {
			manifest = string(f.Data)
		}
	}
	for _, want := range []string{`"command": "node"`, `/mnt/t/index.js`, `"USER_NAME": "alice"`, `"TOKEN": "${TRACKER_TOKEN}"`} {
		if !strings.Contains(strings.ReplaceAll(manifest, `":"`, `": "`), want) {
			t.Errorf("manifest lacks %s:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "s3cret") {
		t.Error("secret value leaked into the manifest")
	}
	if !strings.Contains(strings.Join(plan.Spec.Env, "\n"), "TRACKER_TOKEN=s3cret") {
		t.Error("secret not forwarded through the container environment")
	}
}

func TestSetupStepsGoIntoTheManifest(t *testing.T) {
	proj := newProject(t)
	toml := "[[setup]]\nname=\"venv\"\nrun=\"python -m venv ~/v\"\ncreates=\"~/v/bin/python\"\n"
	plan := testPlan(t, toml, proj, nil)
	for _, f := range plan.Files {
		if f.Path == "/etc/airlock/manifest.json" {
			for _, want := range []string{`"name": "venv"`, `python -m venv ~/v`, `"creates": "~/v/bin/python"`} {
				if !strings.Contains(strings.ReplaceAll(string(f.Data), `":"`, `": "`), want) {
					t.Errorf("manifest lacks %s:\n%s", want, f.Data)
				}
			}
			return
		}
	}
	t.Fatal("no manifest")
}

func TestLiteralEnvValue(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "[env]\nVAULT_DIR = { value = \"/mnt/vault\" }\n", proj, nil)
	if !strings.Contains(strings.Join(plan.Spec.Env, "\n"), "VAULT_DIR=/mnt/vault") {
		t.Errorf("literal env value not set: %v", plan.Spec.Env)
	}
}

func TestConciseSkillShippedByDefaultAndSwitchable(t *testing.T) {
	proj := newProject(t)
	files := func(toml string) map[string]string {
		out := map[string]string{}
		for _, f := range testPlan(t, toml, proj, nil).Files {
			out[f.Path] = string(f.Data)
		}
		return out
	}
	on := files("")
	if !strings.Contains(on["/etc/airlock/skills/concise/SKILL.md"], "name: concise") {
		t.Error("the concise skill is not shipped by default")
	}
	if !strings.Contains(on["/etc/airlock/pi.args"], "--skill\n/etc/airlock/skills/concise\n") {
		t.Errorf("Pi is not told to load it:\n%s", on["/etc/airlock/pi.args"])
	}
	agents := on["/etc/airlock/AGENTS.md"]
	if !strings.Contains(agents, "## Reply style") || !strings.Contains(agents, "Lead with the answer") || strings.Contains(agents, "name: concise") {
		t.Errorf("the rules (without front matter) belong in AGENTS.md:\n%s", agents)
	}

	off := files("[agents]\nconcise = false\n")
	if _, ok := off["/etc/airlock/skills/concise/SKILL.md"]; ok || strings.Contains(off["/etc/airlock/pi.args"], "concise") ||
		strings.Contains(off["/etc/airlock/AGENTS.md"], "Reply style") {
		t.Error("agents.concise = false must remove the skill, the Pi argument and the section")
	}
}

func TestMainSandboxHidesOnlyTheIsolatedWorktrees(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "", proj, nil)
	hidden := proj.Path + "/.worktrees/.isolated"
	opts, ok := plan.Spec.Tmpfs[hidden]
	if !ok || !strings.HasPrefix(opts, "ro,") {
		t.Fatalf("%s must be an empty read-only tmpfs, got %v", hidden, plan.Spec.Tmpfs)
	}
	if _, ok := plan.Spec.Tmpfs[proj.Path+"/.worktrees"]; ok {
		t.Error("the shared worktrees must stay visible in the project's sandbox")
	}
	off := testPlan(t, "[sandbox]\nworktrees_dir=\"\"\n", proj, nil)
	if len(off.Spec.Tmpfs) != 1 {
		t.Errorf("nothing to hide when worktrees are disabled: %v", off.Spec.Tmpfs)
	}
	// Hiding them is part of the policy: sandboxes made before it must be recreated.
	if plan.Policy == off.Policy {
		t.Error("hiding the isolated folder does not change the policy hash")
	}
}

func TestSettleSendsSharedWorktreesToTheProjectsSandbox(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	main, _ := Resolve(root)
	shared := mkWorktree(t, root, "feat")

	// Isolated folder and a worktree elsewhere keep their own sandbox.
	iso := filepath.Join(root, ".worktrees", ".isolated", "rev")
	admin := filepath.Join(root, ".git", "worktrees", "rev")
	os.MkdirAll(iso, 0o755)
	os.MkdirAll(admin, 0o755)
	os.WriteFile(filepath.Join(iso, ".git"), []byte("gitdir: "+admin+"\n"), 0o644)
	isolated, err := Resolve(iso)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(realTemp(t), "elsewhere")
	admin2 := filepath.Join(root, ".git", "worktrees", "elsewhere")
	os.MkdirAll(outside, 0o755)
	os.MkdirAll(admin2, 0o755)
	os.WriteFile(filepath.Join(outside, ".git"), []byte("gitdir: "+admin2+"\n"), 0o644)
	elsewhere, err := Resolve(outside)
	if err != nil {
		t.Fatal(err)
	}

	got := shared.Settle(".worktrees")
	if got.IsWorktree() || got.Root != root || got.ContainerName() != main.ContainerName() || got.Cwd != shared.Root {
		t.Errorf("shared worktree settled to %+v, want the project's sandbox with Cwd %s", got, shared.Root)
	}
	if got.WorkDir() != shared.Path || main.WorkDir() != main.Path {
		t.Errorf("sessions start in %q (worktree) and %q (project)", got.WorkDir(), main.WorkDir())
	}
	for name, p := range map[string]*Project{"isolated": isolated, "elsewhere": elsewhere} {
		if s := p.Settle(".worktrees"); s != p || !s.IsWorktree() {
			t.Errorf("%s worktree must keep its own sandbox: %+v", name, s)
		}
	}
	if s := shared.Settle(""); s != shared {
		t.Error("with worktrees disabled nothing is shared")
	}
	if s := main.Settle(".worktrees"); s != main {
		t.Error("the main checkout settles to itself")
	}
}

func TestSharedWorktreesConfigFilesAreReadOnlyInTheProjectSandbox(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	wt := mkWorktree(t, root, "feat")
	cfg := filepath.Join(root, ".git", "worktrees", "feat", "config.worktree")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	main, _ := Resolve(root)
	_ = wt
	// Git ignores the file unless the extension is on, and then protecting it
	// would only make the policy change whenever any worktree is added.
	before := testPlan(t, "", main, nil)
	if findBind(before, cfg) != nil {
		t.Error("config.worktree protected although git does not read it")
	}
	testWorktreeConfig = "true"
	t.Cleanup(func() { testWorktreeConfig = "" })
	on := testPlan(t, "", main, nil)
	if b := findBind(on, cfg); b == nil || !b.ReadOnly {
		t.Errorf("config.worktree of a worktree must be read-only with the extension on: %+v", b)
	}
	if on.Policy == before.Policy {
		t.Error("protecting it did not change the policy, so existing sandboxes would not be recreated")
	}
}

func TestAddingAnIsolatedWorktreeDoesNotChangeTheProjectsPolicy(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	main, _ := Resolve(root)
	before := testPlan(t, "", main, nil).Policy
	iso := filepath.Join(root, ".worktrees", ".isolated", "rev")
	admin := filepath.Join(root, ".git", "worktrees", "rev")
	os.MkdirAll(iso, 0o755)
	os.MkdirAll(admin, 0o755)
	os.WriteFile(filepath.Join(iso, ".git"), []byte("gitdir: "+admin+"\n"), 0o644)
	p, err := Resolve(iso)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareHost(p, ".worktrees"); err != nil { // creates config.worktree for the worktree
		t.Fatal(err)
	}
	if after := testPlan(t, "", main, nil).Policy; after != before {
		t.Error("creating an isolated worktree makes the project's sandbox look out of date")
	}
}

func TestPrepareHostCreatesTheIsolatedFolder(t *testing.T) {
	proj := newProject(t)
	if _, err := PrepareHost(proj, ".worktrees"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(proj.Root, ".worktrees", ".isolated")); err != nil || !st.IsDir() {
		t.Errorf("the isolated folder is missing (the tmpfs that hides it needs a target): %v", err)
	}
}

func TestWorktreeSandboxSeesOnlyItselfAndTheGitDirectory(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	wt := mkWorktree(t, root, "review-1")
	mkWorktree(t, root, "feature") // a sibling
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o644)

	plan := testPlan(t, "", wt, nil)
	if plan.Spec.Name == "" || plan.Spec.WorkDir != wt.Path {
		t.Errorf("name %q workdir %q", plan.Spec.Name, plan.Spec.WorkDir)
	}
	if b := findBind(plan, wt.Path); b == nil || b.Source != wt.Root || b.ReadOnly {
		t.Errorf("the worktree itself must be mounted read-write: %+v", b)
	}
	gitDir := filepath.Join(root, ".git")
	if b := findBind(plan, gitDir); b == nil || b.Source != gitDir || b.ReadOnly {
		t.Errorf("the shared git directory must be mounted (its .git file points there): %+v", b)
	}
	for _, name := range []string{"hooks", "config"} {
		if b := findBind(plan, filepath.Join(gitDir, name)); b == nil || !b.ReadOnly {
			t.Errorf(".git/%s must be read-only: %+v", name, b)
		}
	}
	for _, bind := range plan.Spec.Binds {
		if bind.Source == root {
			t.Errorf("the main checkout is mounted: %+v", bind)
		}
		if strings.HasPrefix(bind.Source, filepath.Join(root, ".worktrees", "feature")) {
			t.Errorf("a sibling worktree is mounted: %+v", bind)
		}
	}
	if _, ok := plan.Spec.Tmpfs[wt.Path+"/.worktrees"]; ok {
		t.Error("a worktree has no worktrees directory to hide")
	}
	agents := ""
	for _, f := range plan.Files {
		if f.Path == "/etc/airlock/AGENTS.md" {
			agents = string(f.Data)
		}
	}
	if !strings.Contains(agents, "### Worktree\n") || !strings.Contains(agents, "`"+root+"`") {
		t.Errorf("AGENTS.md does not describe the worktree:\n%s", agents)
	}
}

func TestWorktreeConfigFileIsReadOnlyToo(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	wt := mkWorktree(t, root, "w")
	if _, err := PrepareHost(wt, ".worktrees"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, ".git", "worktrees", "w", "config.worktree")
	if !fileExists(cfg) {
		t.Fatal("PrepareHost must create config.worktree so it can be protected")
	}
	if b := findBind(testPlan(t, "", wt, nil), cfg); b == nil || !b.ReadOnly {
		t.Errorf("config.worktree (core.hooksPath, core.fsmonitor) must be read-only: %+v", b)
	}
}

func TestWorktreeSharesTheProjectsCredentialsVolume(t *testing.T) {
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	main, _ := Resolve(root)
	wt := mkWorktree(t, root, "w")
	vol := func(p *Project) string {
		for _, v := range testPlan(t, "[credentials]\nscope=\"per-project\"\n", p, nil).Spec.Volumes {
			if v.Target == "/creds" {
				return v.Name
			}
		}
		return ""
	}
	if vol(main) == "" || vol(main) != vol(wt) {
		t.Errorf("creds volumes differ: main %q, worktree %q (logging in again per worktree would be tedious)", vol(main), vol(wt))
	}
}

func TestWorktreeFallsBackToTheMainAirlockignore(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", realTemp(t))
	root := mkRepo(t, filepath.Join(realTemp(t), "repo"))
	wt := mkWorktree(t, root, "w")
	os.WriteFile(filepath.Join(root, ".airlockignore"), []byte("*.pem\n"), 0o644)
	os.WriteFile(filepath.Join(wt.Root, "k.pem"), []byte("k"), 0o644)
	masks, _ := ignoreMasks(wt)
	found := false
	for _, b := range masks {
		found = found || b.Target == wt.Path+"/k.pem"
	}
	if !found {
		t.Errorf("k.pem not masked in the worktree: %+v", masks)
	}
}

func TestPlanAgentManifestAndClaudeMCP(t *testing.T) {
	proj := newProject(t)
	toml := "[agent]\nkind = \"claude\"\nversion = \"latest\"\nargs = [\"--dangerously-skip-permissions\"]\n" +
		"[[mcp]]\nname = \"docs\"\nurl = \"https://example.com/mcp\"\ndescription = \"Docs\"\n" +
		"[[mcp]]\nname = \"tool\"\ncommand = \"node\"\nargs = [\"/mnt/tool.js\"]\n"
	plan := testPlan(t, toml, proj, nil)
	files := map[string]string{}
	for _, f := range plan.Files {
		files[f.Path] = string(f.Data)
	}
	var man struct {
		Agent struct {
			Kind     string            `json:"kind"`
			Versions map[string]string `json:"versions"`
			Install  []string          `json:"install"`
			Args     []string          `json:"args"`
			Resume   bool              `json:"resume"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(files["/etc/airlock/manifest.json"]), &man); err != nil {
		t.Fatal(err)
	}
	if man.Agent.Kind != "claude" || man.Agent.Versions["claude"] != "latest" || man.Agent.Versions["pi"] != config.PinnedPiVersion ||
		len(man.Agent.Install) != 1 || man.Agent.Install[0] != "claude" || len(man.Agent.Args) != 1 || !man.Agent.Resume {
		t.Errorf("agent manifest: %+v", man.Agent)
	}
	var mcp struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(files["/etc/airlock/claude-mcp.json"]), &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp.Servers["docs"]["type"] != "http" || mcp.Servers["docs"]["url"] != "https://example.com/mcp" || mcp.Servers["docs"]["description"] != nil {
		t.Errorf("remote server: %v", mcp.Servers["docs"])
	}
	if mcp.Servers["tool"]["command"] != "node" || mcp.Servers["tool"]["type"] != nil {
		t.Errorf("stdio server: %v", mcp.Servers["tool"])
	}
	if strings.Contains(files["/etc/airlock/pi.args"], "dangerously") {
		t.Error("Claude Code's arguments leaked into Pi's")
	}
}

func TestPlanPiAgentGetsItsArgsAndInstallsClaudeForTheBridge(t *testing.T) {
	proj := newProject(t)
	plan := testPlan(t, "[agent]\nargs = [\"--thinking\", \"high\"]\n[pi]\npackages = [\"npm:pi-claude-bridge@0.11.0\"]\n", proj, nil)
	for _, f := range plan.Files {
		switch f.Path {
		case "/etc/airlock/pi.args":
			if !strings.HasSuffix(string(f.Data), "--thinking\nhigh\n") {
				t.Errorf("pi.args = %q", f.Data)
			}
		case "/etc/airlock/manifest.json":
			if !strings.Contains(string(f.Data), `"pi",`) || !strings.Contains(string(f.Data), `"claude"`) {
				t.Errorf("a Pi package that needs Claude Code must install it: %s", f.Data)
			}
		}
	}
}
