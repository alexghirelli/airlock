//go:build integration

package tests

import (
	"strings"
	"testing"
)

// extras.mise tools must be on the PATH of every process in the sandbox,
// including the ones Pi spawns for local MCP servers.
func TestMiseToolsAreOnPathForMCPCommands(t *testing.T) {
	p := newProject(t, "misemcp", `
[extras]
mise = ["node@22"]

[[mcp]]
name = "n"
command = "node"
args = ["-e", "console.log('mcp ok')"]
`)
	out := p.mustSh(`command -v node; node -e "console.log('mcp ok')"; echo PATH=$PATH; ls ~/.local/share/mise/shims`)
	if !strings.Contains(out, "mcp ok") {
		t.Fatalf("node is not runnable inside the sandbox:\n%s", out)
	}
}
