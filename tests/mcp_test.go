//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A local (stdio) MCP server: its code comes in through a mount, its entry
// lands in Pi's mcp.json, and the command runs inside the sandbox.
func TestStdioMCPServerFromMount(t *testing.T) {
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "server.sh"), []byte("#!/bin/sh\necho \"hello $WHO from $(uname -s)\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, "mcp", `
[[mount]]
name = "tools"
host = "`+tools+`"
mode = "ro"

[[mcp]]
name = "local"
command = "sh"
args = ["/mnt/tools/server.sh"]
env = { WHO = "alice" }
`)
	out := p.mustSh(`jq -c '.mcpServers.local' ~/.pi/agent/mcp.json; WHO=alice sh /mnt/tools/server.sh`)
	for _, want := range []string{`"command":"sh"`, `"args":["/mnt/tools/server.sh"]`, `"WHO":"alice"`, "hello alice from Linux"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
}
