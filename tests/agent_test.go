//go:build integration

package tests

import (
	"strings"
	"testing"
)

// Claude Code is never in the image. When it is the agent, the sandbox installs it
// with Anthropic's own installer into the tools volume, a folder per version, and
// starts it with the sandbox's instructions, MCP servers, the task of the worktree
// and the configured arguments. Herdr sees it as an agent.
func TestClaudeCodeAsTheAgent(t *testing.T) {
	p := newProject(t, "claude-agent", `
[agent]
kind = "claude"
args = ["--dangerously-skip-permissions"]

[[mcp]]
name = "docs"
url = "https://example.com/mcp"
`)
	out := p.mustSh(`
head -c 2 /usr/local/bin/claude; echo " <- the claude in the image is a script, not a program"
test ! -e /opt/claude && echo no-opt-claude
echo "version: $(claude --version 2>&1 | head -1)"
ls ~/.local/share/mise/agents/claude
AIRLOCK_AGENT_ARGS_ONLY=1 claude
`)
	for _, want := range []string{"#! <-", "no-opt-claude", "version: 2.1.322 (Claude Code)", "2.1.322\n",
		"--dangerously-skip-permissions", "--mcp-config\n/etc/airlock/claude-mcp.json", "--append-system-prompt\n",
		"airlock sandbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The same installer-free commands (login, version) are not given the prompt.
	if out := p.mustSh(`AIRLOCK_AGENT_ARGS_ONLY=1 claude --version`); strings.Contains(out, "append-system-prompt") {
		t.Errorf("claude --version got the sandbox prompt:\n%s", out)
	}
	if got := p.mustSh(`jq -c '.mcpServers.docs' /etc/airlock/claude-mcp.json`); !strings.Contains(got, `"type":"http"`) {
		t.Errorf("claude MCP config: %s", got)
	}

	// Herdr runs it as an agent in the project's workspace.
	p.mustSh(`airlock-herdr-up --agent claude`)
	if agents := p.mustSh(`herdr agent list`); !strings.Contains(agents, `"name":"claude"`) {
		t.Errorf("no claude agent in Herdr:\n%s", agents)
	}
}

// Any Pi version, not only the one in the image, installed on demand.
func TestOtherPiVersionIsInstalledIntoTheVolume(t *testing.T) {
	p := newProject(t, "pi-version", "[agent]\nversion = \"latest\"\n")
	out := p.mustSh(`
ls ~/.local/share/mise/agents/pi/latest/bin/pi
pi --version
echo "image: $(cat /opt/pi/VERSION)"
`)
	if !strings.Contains(out, "agents/pi/latest/bin/pi") || !strings.Contains(out, "image: ") {
		t.Errorf("the requested Pi version was not installed:\n%s", out)
	}
}
