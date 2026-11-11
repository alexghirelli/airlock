//go:build integration

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A first run on a machine without the image downloads the published one instead
// of building it, and falls back to a local build when the download fails. The
// "registry" is a throwaway local one, so this needs no published release.
func TestFirstRunDownloadsThePublishedImage(t *testing.T) {
	const version = "0.0.1-pulltest"
	dir := t.TempDir()
	bin := filepath.Join(dir, "airlock")
	build := exec.Command("go", "build", "-ldflags", "-X github.com/alexghirelli/airlock/internal/version.Version="+version, "-o", bin, "../cmd/airlock")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	name := "airlock-it-registry-" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	_ = exec.Command("docker", "rm", "-f", name).Run()
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::5000", "registry:2").CombinedOutput(); err != nil {
		t.Fatalf("start registry: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	port, err := exec.Command("docker", "port", name, "5000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	hostPort := strings.TrimSpace(string(port))
	hostPort = hostPort[strings.LastIndex(hostPort, ":")+1:]
	registry := "localhost:" + hostPort + "/airlock"

	// "Publish" the image the suite already built (same sources, so same contents).
	published := registry + ":" + version
	for _, args := range [][]string{{"tag", "airlock:0.0.0-test", published}, {"push", published}} {
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", published).Run() })

	run := func(registry string) (string, string, time.Duration) {
		ctx, cancel := ctxMinutes(10)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "build", "--if-missing")
		cmd.Env = append(filteredEnv("AIRLOCK_BUILD_LOCAL"), "AIRLOCK_CONFIG="+filepath.Join(dir, "none.toml"), "AIRLOCK_PRUNE_IMAGES=0", "AIRLOCK_IMAGE_REGISTRY="+registry)
		start := time.Now()
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("airlock build --if-missing: %v\n%s", err, stderr.String())
		}
		return strings.TrimSpace(string(out)), stderr.String(), time.Since(start)
	}
	removeLocal := func(ref string) {
		_ = exec.Command("docker", "rmi", ref, "airlock:"+version).Run()
	}

	// Download.
	ref, log, took := run(registry)
	t.Cleanup(func() { removeLocal(ref) })
	if !strings.Contains(log, "downloading the airlock image") || strings.Contains(log, "building the airlock image") {
		t.Errorf("expected a download, got:\n%s", log)
	}
	if out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", ref, "-c", "pi --version && herdr --version").CombinedOutput(); err != nil {
		t.Errorf("the downloaded image does not run: %v\n%s", err, out)
	}
	t.Logf("first run took %s:\n%s", took.Round(time.Millisecond), log)
	if took > time.Minute {
		t.Errorf("the download took %s, it should be well under a minute", took)
	}

	if err := exec.Command("docker", "image", "inspect", published).Run(); err == nil {
		t.Errorf("%s was left behind under its registry name", published)
	}

	// Nothing to do the second time.
	if _, log, _ := run(registry); strings.Contains(log, "downloading") || strings.Contains(log, "building the airlock image") {
		t.Errorf("an image that exists was fetched or built again:\n%s", log)
	}

	// An unreachable registry falls back to a local build.
	removeLocal(ref)
	if _, log, _ := run("localhost:1/airlock"); !strings.Contains(log, "could not download") || !strings.Contains(log, "building the airlock image") {
		t.Errorf("expected a fallback to a local build, got:\n%s", log)
	}
	if err := exec.Command("docker", "image", "inspect", ref).Run(); err != nil {
		t.Errorf("the fallback did not leave %s behind: %v", ref, err)
	}
}

// filteredEnv is the environment without the named variables.
func filteredEnv(drop ...string) []string {
	var env []string
next:
	for _, kv := range os.Environ() {
		for _, d := range drop {
			if strings.HasPrefix(kv, d+"=") {
				continue next
			}
		}
		env = append(env, kv)
	}
	return env
}
