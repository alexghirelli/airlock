package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/imagebuild"
	"github.com/alexghirelli/airlock/internal/version"
)

// ReadyFile appears inside the sandbox once its init script has finished.
const ReadyFile = "/run/airlock/ready"

// Manager starts, enters and removes the sandbox of one project.
type Manager struct {
	B       backend.Backend
	Config  *config.Result
	Project *Project
	Host    Host

	// Log receives progress messages (stderr).
	Log io.Writer
	// Verbose also streams image build output.
	Verbose bool
	// Recreate recreates the sandbox when its fixed settings changed, instead
	// of asking.
	Recreate bool
	// Confirm asks the user a yes/no question. Nil means "no".
	Confirm func(prompt string) bool
	// ReadyTimeout bounds the wait for init (first starts install packages).
	ReadyTimeout time.Duration
}

func (m *Manager) logf(format string, a ...any) {
	if m.Log != nil {
		fmt.Fprintf(m.Log, format+"\n", a...)
	}
}

// Name is the sandbox container name.
func (m *Manager) Name() string { return m.Project.ContainerName() }

// BuildMode says how EnsureImage treats images that already exist.
type BuildMode int

const (
	// BuildIfMissing builds only what does not exist yet.
	BuildIfMissing BuildMode = iota
	// BuildForce rebuilds, using the layer cache.
	BuildForce
	// BuildFresh rebuilds from scratch: no cache, newest base layers.
	BuildFresh
)

// EnsureImage makes sure the base image (and the project image, if the project
// needs one) exists, building whatever is missing, and returns the image the
// sandbox runs.
func (m *Manager) EnsureImage(ctx context.Context, mode BuildMode) (plan imagePlan, err error) {
	c := &m.Config.Config
	plan.Base = imagebuild.BaseRef(c)
	if err := m.ensureBase(ctx, plan.Base, mode); err != nil {
		return plan, err
	}
	plan.Run = plan.Base

	dockerfile := m.projectDockerfile()
	ref := imagebuild.ProjectRef(m.Project.Slug, plan.Base, m.Config.ProjectExtras.Apt, dockerfile)
	if ref == "" {
		m.pruneOldImages(ctx, plan)
		return plan, nil
	}
	if mode == BuildIfMissing {
		if ok, err := m.B.ImageExists(ctx, ref); err != nil {
			return plan, err
		} else if ok {
			plan.Run = ref
			return plan, nil
		}
	}
	m.logf("airlock: building the project image (%s)", ref)
	from := plan.Base
	if dockerfile != "" {
		tag := ref
		if len(m.Config.ProjectExtras.Apt) > 0 {
			tag = ref + "-df"
		}
		dctx, dfName, err := m.projectBuildContext(dockerfile)
		if err != nil {
			return plan, err
		}
		if err := m.build(ctx, backend.BuildRequest{
			Context: dctx, Tags: []string{tag}, Dockerfile: dfName,
			Args: map[string]string{"AIRLOCK_BASE": plan.Base}, NoCache: mode == BuildFresh,
		}); err != nil {
			return plan, err
		}
		from = tag
	}
	if len(m.Config.ProjectExtras.Apt) > 0 {
		ectx, err := imagebuild.SingleFileContext(imagebuild.ExtrasDockerfile(from, m.Config.ProjectExtras.Apt))
		if err != nil {
			return plan, err
		}
		if err := m.build(ctx, backend.BuildRequest{Context: ectx, Tags: []string{ref}, Dockerfile: "Dockerfile", NoCache: mode == BuildFresh}); err != nil {
			return plan, err
		}
	}
	plan.Run = ref
	m.pruneOldImages(ctx, plan)
	return plan, nil
}

// pruneOldImages untags the airlock images this build superseded, so rebuilding
// (after an upgrade or a config change) does not pile up gigabytes. Images that
// a sandbox still uses are left alone by the engine.
func (m *Manager) pruneOldImages(ctx context.Context, keep imagePlan) {
	if os.Getenv("AIRLOCK_PRUNE_IMAGES") == "0" {
		return // the test suite builds several image variants and keeps them all
	}
	current := map[string]bool{keep.Base: true, keep.Run: true, "airlock:" + version.Tag(): true}
	for _, repo := range []string{"airlock", "airlock-project"} {
		tags, err := m.B.ImageTags(ctx, repo)
		if err != nil {
			return
		}
		for _, t := range tags {
			if current[t] || strings.Contains(t, "-df") && current[strings.TrimSuffix(t, "-df")] {
				continue
			}
			if repo == "airlock-project" && !strings.HasPrefix(t, "airlock-project:"+m.Project.Slug+"-") {
				continue // another project's image
			}
			_ = m.B.RemoveImageTag(ctx, t)
		}
	}
	// Untagged leftovers of earlier builds (a sandbox held them until now).
	_ = m.B.PruneImages(ctx)
}

type imagePlan struct{ Base, Run string }

func (m *Manager) ensureBase(ctx context.Context, ref string, mode BuildMode) error {
	if mode == BuildIfMissing {
		ok, err := m.B.ImageExists(ctx, ref)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		m.logf("airlock: building the airlock image (first run only; this takes a few minutes)")
	}
	bctx, err := imagebuild.BaseContext()
	if err != nil {
		return err
	}
	c := &m.Config.Config
	return m.build(ctx, backend.BuildRequest{
		Context: bctx, Tags: []string{ref, "airlock:" + version.Tag()}, Dockerfile: "Dockerfile",
		Args: imagebuild.BaseArgs(c), NoCache: mode == BuildFresh, Pull: mode == BuildFresh,
	})
}

// build runs an image build, showing the log only when verbose or on failure.
func (m *Manager) build(ctx context.Context, req backend.BuildRequest) error {
	var tail bytes.Buffer
	if m.Verbose && m.Log != nil {
		req.Progress = m.Log
	} else {
		req.Progress = &tail
	}
	if err := m.B.BuildImage(ctx, req); err != nil {
		if !m.Verbose {
			return fmt.Errorf("%w\n--- last build output ---\n%s", err, lastLines(tail.String(), 25))
		}
		return err
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// projectDockerfile returns the project Dockerfile path relative to the project
// root, or "" if there is none.
func (m *Manager) projectDockerfile() string {
	rel := m.Config.Config.Image.Dockerfile
	if rel == "" {
		rel = ".airlock/Dockerfile"
	}
	if st, err := os.Stat(filepath.Join(m.Project.Root, filepath.FromSlash(rel))); err == nil && st.Mode().IsRegular() {
		return rel
	}
	return ""
}

// projectBuildContext tars the Dockerfile's directory. Symlinks and special
// files are skipped, so the project cannot make the build read host files
// outside its own folder.
func (m *Manager) projectBuildContext(rel string) (io.Reader, string, error) {
	abs := filepath.Join(m.Project.Root, filepath.FromSlash(rel))
	dir := filepath.Dir(abs)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, "", err
	}
	if r, err := filepath.Rel(m.Project.Root, real); err != nil || strings.HasPrefix(r, "..") {
		return nil, "", fmt.Errorf("%s resolves outside the project", rel)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	const maxContext = 256 << 20
	var total int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > maxContext {
			return fmt.Errorf("build context %s is larger than %d MiB", dir, maxContext>>20)
		}
		name, _ := filepath.Rel(dir, p)
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, filepath.ToSlash(filepath.Base(abs)), nil
}

// Up makes the sandbox exist, run and be ready. It reports whether the
// container was created by this call.
func (m *Manager) Up(ctx context.Context) (created bool, err error) {
	c := &m.Config.Config
	if c.Network.Mode == "allowlist" {
		return false, errors.New("network.mode \"allowlist\" is not implemented yet; use \"open-no-lan\" (the default)")
	}
	if err := m.B.Ping(ctx); err != nil {
		return false, err
	}
	if info, err := m.B.Info(ctx); err == nil {
		m.Host.SELinux = info.SELinux
	}

	images, err := m.EnsureImage(ctx, BuildIfMissing)
	if err != nil {
		return false, err
	}
	if err := m.B.EnsureNetwork(ctx, NetworkName); err != nil {
		return false, err
	}

	hostWarn, err := PrepareHost(m.Project, c.Sandbox.WorktreesDir)
	if err != nil {
		return false, err
	}
	plan, err := BuildPlan(PlanInput{Project: m.Project, Config: m.Config, Host: m.Host, Image: images.Run})
	if err != nil {
		return false, err
	}
	for _, w := range append(hostWarn, plan.Warnings...) {
		m.logf("airlock: warning: %s", w)
	}
	if c.Network.Mode == "open" {
		m.logf("airlock: WARNING network mode 'open': the sandbox can reach your computer and your LAN")
	}
	for _, v := range plan.Spec.Volumes {
		if err := m.B.EnsureVolume(ctx, v.Name); err != nil {
			return false, err
		}
	}

	name := m.Name()
	info, err := m.B.Inspect(ctx, name)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		info = nil
	case err != nil:
		return false, err
	}

	if info != nil {
		recreate, err := m.needsRecreate(info, plan)
		if err != nil {
			return false, err
		}
		if recreate {
			m.logf("airlock: recreating %s", name)
			if err := m.B.Remove(ctx, name, true); err != nil && !errors.Is(err, backend.ErrNotFound) {
				return false, err
			}
			info = nil
			// The old sandbox no longer pins its image; free it now.
			m.pruneOldImages(ctx, images)
		}
	}

	if info == nil {
		m.logf("airlock: creating %s for %s", name, m.Project.Root)
		if err := m.B.Create(ctx, plan.Spec); err != nil {
			return false, err
		}
		created = true
	} else if info.Running {
		return false, m.waitReady(ctx, name)
	} else {
		m.logf("airlock: starting %s", name)
	}
	if err := m.B.CopyTo(ctx, name, plan.Files); err != nil {
		return created, err
	}
	if err := m.B.Start(ctx, name); err != nil {
		return created, fmt.Errorf("start %s: %w", name, err)
	}
	return created, m.waitReady(ctx, name)
}

// needsRecreate decides whether an existing sandbox must be rebuilt because a
// setting that is fixed at creation changed.
func (m *Manager) needsRecreate(info *backend.Info, plan *Plan) (bool, error) {
	if info.Labels[backend.LabelBackend] != "" && info.Labels[backend.LabelBackend] != m.Config.Config.Sandbox.Backend {
		return false, fmt.Errorf("%s was created with the %q backend; run `airlock rm` first to switch to %q (installed tools in the sandbox are lost)",
			m.Name(), info.Labels[backend.LabelBackend], m.Config.Config.Sandbox.Backend)
	}
	if m.Config.Config.Sandbox.Lifetime == "ephemeral" && !info.Running {
		return true, nil
	}
	if info.Labels[backend.LabelPolicy] == plan.Policy {
		return false, nil
	}
	if m.Recreate {
		return true, nil
	}
	msg := fmt.Sprintf("The configuration of %s changed in a way that needs the sandbox recreated (network, mounts, resources, image or environment). "+
		"Installed tools inside it are lost; your project files are not touched. Recreate it now?", m.Name())
	if m.Confirm != nil && m.Confirm(msg) {
		return true, nil
	}
	return false, errors.New("sandbox configuration changed; run `airlock rm` (or pass --recreate) to apply it")
}

func (m *Manager) waitReady(ctx context.Context, name string) error {
	timeout := m.ReadyTimeout
	if timeout == 0 {
		timeout = 20 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	noticed := false
	start := time.Now()
	for {
		info, err := m.B.Inspect(ctx, name)
		if err != nil {
			return err
		}
		if !info.Running {
			logs, _ := m.B.Logs(ctx, name, 30)
			return fmt.Errorf("%s stopped during startup:\n%s", name, strings.TrimSpace(logs))
		}
		code, err := m.B.Exec(ctx, name, backend.ExecOpts{Cmd: []string{"test", "-f", ReadyFile}})
		if err == nil && code == 0 {
			return nil
		}
		if !noticed && time.Since(start) > 2*time.Second {
			m.logf("airlock: preparing the sandbox...")
			noticed = true
		}
		if time.Now().After(deadline) {
			logs, _ := m.B.Logs(ctx, name, 30)
			return fmt.Errorf("%s did not become ready in %s:\n%s", name, timeout, strings.TrimSpace(logs))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Command returns the command line for a UI.
func Command(ui string) []string {
	switch ui {
	case "pi":
		return []string{"pi"}
	case "shell":
		return []string{"bash", "-l"}
	}
	return []string{"herdr"}
}

// SessionEnv is the environment forwarded to an interactive session: the
// terminal type and nothing else from the host.
func SessionEnv(getenv func(string) string) []string {
	env := []string{"HOME=/home/agent", "USER=agent"}
	term := getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	env = append(env, "TERM="+term)
	if v := getenv("COLORTERM"); v != "" {
		env = append(env, "COLORTERM="+v)
	}
	return env
}

// SessionWrapper starts a session as the unprivileged user with the firewall
// capabilities dropped. `docker exec` processes get the container's configured
// capabilities (not the entrypoint's reduced set), so every session must go
// through it; running exec directly as `agent` would leave NET_ADMIN reachable
// through `sudo`.
const SessionWrapper = "/usr/local/bin/airlock-session"

func sessionCmd(cmd []string) []string { return append([]string{SessionWrapper}, cmd...) }

// Attach opens an interactive session in the sandbox.
func (m *Manager) Attach(ctx context.Context, cmd []string) (int, error) {
	return m.B.Attach(ctx, m.Name(), backend.ExecOpts{
		Cmd: sessionCmd(cmd), User: "root", WorkDir: m.Project.Path, Env: SessionEnv(m.Host.Getenv),
	})
}

// PrepareHerdr makes sure the project's Herdr session has Pi (or, with team,
// the team lead and its specialists) running before the UI attaches.
func (m *Manager) PrepareHerdr(ctx context.Context, team bool) error {
	cmd := []string{"airlock-herdr-up"}
	if team {
		cmd = append(cmd, "--team")
	}
	var out bytes.Buffer
	code, err := m.Exec(ctx, cmd, nil, &out, &out)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("could not set up Herdr (exit %d):\n%s", code, strings.TrimSpace(out.String()))
	}
	return nil
}

// Exec runs a non-interactive command in the sandbox.
func (m *Manager) Exec(ctx context.Context, cmd []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return m.B.Exec(ctx, m.Name(), backend.ExecOpts{
		Cmd: sessionCmd(cmd), User: "root", WorkDir: m.Project.Path, Env: SessionEnv(m.Host.Getenv),
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	})
}

// Stop stops the sandbox. It is not an error if it does not exist.
func (m *Manager) Stop(ctx context.Context) error {
	err := m.B.Stop(ctx, m.Name())
	if errors.Is(err, backend.ErrNotFound) {
		return nil
	}
	return err
}

// Remove deletes the sandbox container (volumes are kept).
func (m *Manager) Remove(ctx context.Context) error {
	err := m.B.Remove(ctx, m.Name(), true)
	if errors.Is(err, backend.ErrNotFound) {
		return nil
	}
	return err
}
