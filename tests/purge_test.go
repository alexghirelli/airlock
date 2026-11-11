//go:build integration

package tests

import (
	"os/exec"
	"strings"
	"testing"
)

func volumesNamed(t *testing.T, prefix string) []string {
	t.Helper()
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+prefix).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}

// `airlock rm` keeps a project's own credentials volume, so recreating the sandbox does
// not mean logging in again; `rm --purge` deletes it. The shared volume is never touched.
func TestRmKeepsPerProjectCredentialsUnlessPurged(t *testing.T) {
	p := newProject(t, "purge-me", "[credentials]\nscope = \"per-project\"\n")
	p.mustSh("true")
	vols := volumesNamed(t, "airlock-creds-purge-me-")
	if len(vols) != 1 {
		t.Fatalf("expected one per-project credentials volume, got %v", vols)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", vols[0]).Run() })
	ctx, cancel := ctxMinutes(2)
	defer cancel()

	if _, stderr, code := p.run(ctx, nil, "rm"); code != 0 || !strings.Contains(stderr, "removed") {
		t.Fatalf("rm: exit %d\n%s", code, stderr)
	}
	if got := volumesNamed(t, "airlock-creds-purge-me-"); len(got) != 1 {
		t.Errorf("a plain rm deleted the credentials volume: %v", got)
	}
	if _, stderr, code := p.run(ctx, nil, "rm", "--purge"); code != 0 || !strings.Contains(stderr, "deleted "+vols[0]) {
		t.Fatalf("rm --purge: exit %d\n%s", code, stderr)
	}
	if got := volumesNamed(t, "airlock-creds-purge-me-"); len(got) != 0 {
		t.Errorf("rm --purge left the volume: %v", got)
	}
}

func TestPurgeLeavesTheSharedCredentialsVolume(t *testing.T) {
	p := newProject(t, "purge-shared", "")
	p.mustSh("true")
	ctx, cancel := ctxMinutes(2)
	defer cancel()
	_, stderr, code := p.run(ctx, nil, "rm", "--purge")
	if code != 0 || !strings.Contains(stderr, "shared by every project") {
		t.Errorf("rm --purge with the shared volume: exit %d\n%s", code, stderr)
	}
	if got := volumesNamed(t, "airlock-creds"); len(got) == 0 {
		t.Error("the shared credentials volume was deleted")
	}
}

// Herdr's welcome screens are skipped in a new sandbox.
func TestHerdrSkipsItsFirstRunScreens(t *testing.T) {
	p := newProject(t, "herdr-onboarding", "")
	if out := p.mustSh("cat ~/.config/herdr/config.toml"); !strings.Contains(out, "onboarding = false") {
		t.Errorf("Herdr config: %q", out)
	}
}
