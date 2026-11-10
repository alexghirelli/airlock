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
mise = ["node@22", "python@3.12"]

[[mcp]]
name = "n"
command = "node"
args = ["-e", "console.log('mcp ok')"]
`)
	out := p.mustSh(`node -e "console.log('node ok')"; python -c "print('python ok')"; ls ~/.local/share/mise/shims`)
	// `airlock shell` is a login shell, which on Debian resets PATH.
	out += p.mustSh(`bash -lc "python -c \"print('login python ok')\"; node -e \"console.log('login node ok')\""`)
	for _, want := range []string{"node ok", "python ok", "login python ok", "login node ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("every extras.mise tool must be installed; missing %q:\n%s", want, out)
		}
	}
}
