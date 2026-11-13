//go:build integration

package tests

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// `airlock gc` lists the sandbox and the own credentials volume of a project whose
// folder is gone, and nothing of a project that is still there. It only lists:
// --force would also collect every other leftover on the machine running the suite.
func TestGCFindsWhatADeletedProjectLeftBehind(t *testing.T) {
	p := newProject(t, "gc-gone", perProjectLogins)
	purgeAtEnd(t, p)
	p.mustSh(`true`) // starts the sandbox and creates the credentials volume
	name := p.containerName()

	gc := func() string {
		t.Helper()
		cmd := exec.Command(airlockBin, "gc")
		cmd.Dir = os.TempDir() // the project folder is not there in the middle of this test
		cmd.Env = p.env()
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("airlock gc: %v", err)
		}
		return out.String()
	}
	if out := gc(); strings.Contains(out, name) || strings.Contains(out, "airlock-creds-gc-gone-") {
		t.Fatalf("gc lists a project that is still there:\n%s", out)
	}

	away := p.Dir + ".away"
	if err := os.Rename(p.Dir, away); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(away, p.Dir) }) // runs before the purge registered above
	out := gc()
	for _, want := range []string{name, p.Dir + " (running)", "airlock-creds-gc-gone-"} {
		if !strings.Contains(out, want) {
			t.Errorf("gc does not list %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "would remove") || strings.Contains(out, "removing") {
		t.Errorf("without --force gc must only list:\n%s", out)
	}

	if err := os.Rename(away, p.Dir); err != nil {
		t.Fatal(err)
	}
	if out := gc(); strings.Contains(out, name) {
		t.Errorf("gc still lists the project after its folder came back:\n%s", out)
	}
}
