//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A running sandbox that no longer matches the config says so, and
// `airlock restart` applies the change.
func TestConfigDriftIsReportedAndRestartApplies(t *testing.T) {
	p := newProject(t, "drift", "")
	ctx, cancel := ctxMinutes(5)
	defer cancel()
	p.mustSh("true") // creates and starts the sandbox

	_, errOut, _ := p.run(ctx, nil, "exec", "--", "true")
	if strings.Contains(errOut, "configuration changed") {
		t.Fatalf("no change yet, but airlock reported one:\n%s", errOut)
	}

	if err := os.WriteFile(p.cfg, []byte("[[setup]]\nname = \"later\"\nrun = \"touch ~/later\"\ncreates = \"~/later\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, _ = p.run(ctx, nil, "exec", "--", "true")
	if !strings.Contains(errOut, "airlock restart") {
		t.Errorf("config drift not reported:\n%s", errOut)
	}

	// restart attaches a UI, which needs a terminal; with --ui shell it still
	// stops, starts and applies the config before it notices there is none.
	p.run(ctx, nil, "restart", "--ui", "shell")
	if out := p.mustSh(`test -f ~/later && echo applied`); !strings.Contains(out, "applied") {
		t.Errorf("restart did not apply the new setup step:\n%s", out)
	}
	// The UI is part of what the sandbox was started with, so keep the flag.
	_, errOut, _ = p.run(ctx, nil, "exec", "--ui", "shell", "--", "true")
	if strings.Contains(errOut, "configuration changed") {
		t.Errorf("drift still reported after restart:\n%s", errOut)
	}
}

// A setup step with watch runs again when the watched files change.
func TestSetupWatchRerunsOnChange(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, "watch", `
[[mount]]
name = "src"
host = "`+src+`"
mode = "ro"

[[setup]]
name = "copy"
run = "echo ran >> ~/runs && cp /mnt/src/app.txt ~/app.copy"
creates = "~/app.copy"
watch = ["/mnt/src"]
`)
	ctx, cancel := ctxMinutes(5)
	defer cancel()
	p.mustSh("true")
	count := func() string { return strings.TrimSpace(p.mustSh(`wc -l < ~/runs`)) }
	if got := count(); got != "1" {
		t.Fatalf("first start should run the step once, got %s", got)
	}
	p.run(ctx, nil, "stop")
	p.mustSh("true")
	if got := count(); got != "1" {
		t.Errorf("nothing changed, but the step ran again (%s runs)", got)
	}

	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("two, longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.run(ctx, nil, "stop")
	p.mustSh("true")
	if got := count(); got != "2" {
		t.Errorf("a watched file changed, expected a second run, got %s", got)
	}
	if got := strings.TrimSpace(p.mustSh(`cat ~/app.copy`)); got != "two, longer" {
		t.Errorf("copy not refreshed: %q", got)
	}
}
