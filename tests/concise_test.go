//go:build integration

package tests

import (
	"strings"
	"testing"
)

// Every sandbox ships the built-in concise skill, and the rules reach /AGENTS.md.
func TestConciseSkillIsInTheSandbox(t *testing.T) {
	p := newProject(t, "concise", "")
	out := p.mustSh(`head -3 /etc/airlock/skills/concise/SKILL.md; cat /etc/airlock/pi.args; grep -A2 'Reply style' /AGENTS.md`)
	for _, want := range []string{"name: concise", "--skill", "/etc/airlock/skills/concise", "Reply style", "Lead with the answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
