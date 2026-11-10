//go:build integration

package tests

import (
	"strings"
	"testing"
)

// Pi packages and Claude Code (spec section 6/8): a configured package is
// installed into the credentials volume, a package that needs Claude Code makes
// the image include it, the default model reaches Pi, and provider keys reach
// the sandbox from the host environment.
func TestPackagesDefaultModelAndEnv(t *testing.T) {
	p := newProject(t, "pkgs", `
[pi]
packages = ["npm:pi-claude-bridge@0.11.0"]
default_model = "claude-bridge/claude-opus-5-5"
[env]
PROVIDER_KEY = { from_host_env = "AIRLOCK_IT_KEY" }
`)
	p.extra = []string{"AIRLOCK_IT_KEY=abc-from-host"}
	out := p.mustSh(`
echo "claude: $(claude --version 2>&1 | head -1)"
pi list 2>&1
cat /etc/airlock/pi.args
echo "key=$PROVIDER_KEY"
echo "models: $(pi --list-models claude-bridge 2>&1 | grep -c 'claude-opus-5-5')"
test -e /AGENTS.md && echo agents-md-present
test -e ~/.pi/agent/AGENTS.md && echo SHARED-AGENTS-MD || echo no-shared-agents-md
`)
	for _, want := range []string{"Claude Code", "npm:pi-claude-bridge@0.11.0", "--model\nclaude-bridge/claude-opus-5-5",
		"key=abc-from-host", "models: 1", "agents-md-present", "no-shared-agents-md"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Two sandboxes sharing the credentials volume must not see each other's
// per-project defaults.
func TestPerProjectDefaultsDoNotLeakThroughSharedVolume(t *testing.T) {
	a := newProject(t, "leaka", "[pi]\ndefault_model = \"prov-a/model-a\"\n")
	b := newProject(t, "leakb", "[pi]\ndefault_model = \"prov-b/model-b\"\n")
	a.mustSh("true")
	b.mustSh("true")
	if out := a.mustSh("cat /etc/airlock/pi.args; grep -c model- ~/.pi/agent/settings.json || true"); !strings.Contains(out, "prov-a/model-a") || strings.Contains(out, "model-b") {
		t.Errorf("project A: %s", out)
	}
	if out := b.mustSh("grep -c model- ~/.pi/agent/settings.json || true"); strings.TrimSpace(out) != "0" {
		t.Errorf("default model leaked into the shared settings.json:\n%s", out)
	}
}
