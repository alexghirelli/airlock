//go:build integration

// Package tests holds the end-to-end acceptance tests (spec section 12). They
// need a running Docker engine and internet access, and build a real sandbox:
//
//	go test -tags integration -timeout 30m ./tests/...
package tests

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var airlockBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "airlock-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	airlockBin = filepath.Join(dir, "airlock")
	// The suite builds its image locally; the pull path has its own test.
	os.Setenv("AIRLOCK_BUILD_LOCAL", "1")
	// A fixed version keeps the image tag stable across commits, so CI can cache
	// the image (the tag also hashes the image sources).
	build := exec.Command("go", "build", "-ldflags", "-X github.com/alexghirelli/airlock/internal/version.Version=0.0.0-test",
		"-o", airlockBin, "../cmd/airlock")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build airlock:", err)
		os.Exit(1)
	}
	// Build the image once; every test then starts from it.
	img := exec.Command(airlockBin, "build", "--if-missing")
	img.Env = append(os.Environ(), "AIRLOCK_CONFIG="+filepath.Join(dir, "none.toml"), "AIRLOCK_PRUNE_IMAGES=0")
	if out, err := img.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "airlock build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// project is a throwaway git repository with its own airlock config.
type project struct {
	t   *testing.T
	Dir string
	cfg string
	// extra is added to the environment of every airlock invocation.
	extra []string
}

// newProject creates a git repo in a fresh temp dir. userToml is the user config.
func newProject(t *testing.T, name, userToml string) *project {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &project{t: t, Dir: dir, cfg: filepath.Join(base, "user.toml")}
	if err := os.WriteFile(p.cfg, []byte(userToml), 0o644); err != nil {
		t.Fatal(err)
	}
	p.git("init", "-q", "-b", "main")
	t.Cleanup(func() {
		// Remove the sandbox and the per-project volume, whatever the test did.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _, _ = p.run(ctx, nil, "rm")
	})
	return p
}

func (p *project) git(args ...string) string {
	p.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", p.Dir, "-c", "user.name=Test", "-c", "user.email=test@example.org"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (p *project) env(extra ...string) []string {
	env := append(os.Environ(), "AIRLOCK_PRUNE_IMAGES=0", "AIRLOCK_CONFIG="+p.cfg, "GIT_CONFIG_GLOBAL="+filepath.Join(filepath.Dir(p.cfg), "gitconfig"))
	// A global git identity for the sandbox to copy.
	_ = os.WriteFile(filepath.Join(filepath.Dir(p.cfg), "gitconfig"), []byte("[user]\n\tname = Test User\n\temail = test@example.org\n"), 0o644)
	env = append(env, p.extra...)
	return append(env, extra...)
}

// run executes airlock in the project directory.
func (p *project) run(ctx context.Context, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	cmd := exec.CommandContext(ctx, airlockBin, args...)
	cmd.Dir = p.Dir
	cmd.Env = p.env(extraEnv...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		e.WriteString(err.Error())
	}
	return o.String(), e.String(), code
}

// sh runs a shell script inside the sandbox and returns its combined output
// and exit code. sudo's audit noise is filtered out.
func (p *project) sh(script string) (string, int) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, errOut, code := p.run(ctx, nil, "exec", "--", "bash", "-c", script)
	return filterNoise(out + errOut), code
}

func filterNoise(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "unable to send audit message") || strings.HasPrefix(l, "airlock: ") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

func (p *project) mustSh(script string) string {
	p.t.Helper()
	out, code := p.sh(script)
	if code != 0 {
		p.t.Fatalf("command failed (%d): %s\n%s", code, script, out)
	}
	return out
}

// containerName returns the sandbox container name of the project.
func (p *project) containerName() string {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, _, _ := p.run(ctx, nil, "ls", "--json")
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `"name"`) {
			name := strings.Trim(strings.TrimSpace(strings.SplitN(l, ":", 2)[1]), `",`)
			if strings.Contains(out, p.Dir) && strings.HasPrefix(name, "airlock-"+filepath.Base(p.Dir)+"-") {
				return name
			}
		}
	}
	p.t.Fatalf("no sandbox found for %s in:\n%s", p.Dir, out)
	return ""
}

// rootExec runs a command in the sandbox as real root with the container's
// full capability set (what a session never gets), for inspecting the firewall.
func (p *project) rootExec(args ...string) string {
	p.t.Helper()
	out, err := exec.Command("docker", append([]string{"exec", "-u", "root", p.containerName()}, args...)...).CombinedOutput()
	if err != nil {
		p.t.Fatalf("docker exec %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func ctxMinutes(n int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(n)*time.Minute)
}
