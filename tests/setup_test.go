//go:build integration

package tests

import (
	"strings"
	"testing"
)

// [[setup]] steps run once, are skipped while done, and a failing step is
// reported without stopping the sandbox.
func TestSetupSteps(t *testing.T) {
	p := newProject(t, "setup", `
[[setup]]
name = "make-thing"
run = "echo ran >> ~/ran && mkdir -p ~/thing && touch ~/thing/made"
creates = "~/thing/made"

[[setup]]
name = "broken"
run = "echo nope >&2; exit 3"
`)
	ctx, cancel := ctxMinutes(5)
	defer cancel()
	_, errOut, code := p.run(ctx, nil, "exec", "--", "true")
	if code != 0 {
		t.Fatalf("a failing setup step must not stop the sandbox (exit %d):\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "setup step 'broken' failed") {
		t.Errorf("failure not shown by the CLI:\n%s", errOut)
	}
	if out := p.mustSh(`test -f ~/thing/made && wc -l < ~/ran`); strings.TrimSpace(out) != "1" {
		t.Errorf("step should have run once, ran file has %q lines", out)
	}

	// Another start: done steps are skipped, the broken one retried.
	p.run(ctx, nil, "stop")
	_, errOut, _ = p.run(ctx, nil, "exec", "--", "true")
	if out := p.mustSh(`wc -l < ~/ran`); strings.TrimSpace(out) != "1" {
		t.Errorf("a finished step ran again: %q", out)
	}
	if !strings.Contains(errOut, "setup step 'broken' failed") {
		t.Errorf("failed step not retried/reported:\n%s", errOut)
	}

	// Deleting what it creates makes it run again.
	p.mustSh(`rm ~/thing/made`)
	p.run(ctx, nil, "stop")
	p.run(ctx, nil, "exec", "--", "true")
	if out := p.mustSh(`test -f ~/thing/made && wc -l < ~/ran`); strings.TrimSpace(out) != "2" {
		t.Errorf("step did not rerun after its output vanished: %q", out)
	}
}
