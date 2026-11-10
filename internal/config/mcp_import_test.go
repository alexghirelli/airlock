package config

import (
	"strings"
	"testing"
)

const importSample = `{
  "mcpServers": {
    "phabricator": {
      "type": "stdio",
      "command": "node",
      "args": ["/Users/me/tools/mcp-phabricator/src/index.js"],
      "env": { "PHABRICATOR_USERNAME": "alice", "PHABRICATOR_API_TOKEN": "api-xyz" }
    },
    "Gerrit MCP": {
      "command": "/Users/me/tools/gerrit/.venv/bin/python",
      "args": ["/Users/me/tools/gerrit/main.py", "stdio"]
    },
    "docs": { "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer abc" } },
    "junk": { "note": "not a server" }
  }
}`

func TestImportMCPJSON(t *testing.T) {
	out, warns, err := ImportMCPJSON([]byte(importSample))
	if err != nil {
		t.Fatal(err)
	}
	// The result must be a valid airlock config on its own.
	c := Defaults()
	if err := decodeStrict([]byte(out), &c); err != nil {
		t.Fatalf("output does not decode:\n%s\n%v", out, err)
	}
	normalize(&c)
	if err := c.Validate(); err != nil {
		t.Fatalf("output is not valid:\n%s\n%v", out, err)
	}
	if len(c.MCP) != 3 {
		t.Fatalf("want 3 servers (junk skipped), got %d:\n%s", len(c.MCP), out)
	}
	byName := map[string]MCP{}
	for _, m := range c.MCP {
		byName[m.Name] = m
	}
	ph := byName["phabricator"]
	if ph.Command != "node" || ph.Env["PHABRICATOR_USERNAME"] != "alice" || ph.EnvFromHost["PHABRICATOR_API_TOKEN"] != "PHABRICATOR_API_TOKEN" {
		t.Errorf("phabricator: %+v", ph)
	}
	if _, ok := byName["gerrit-mcp"]; !ok {
		t.Errorf("name not sanitized: %v", byName)
	}
	if byName["docs"].URL != "https://example.com/mcp" || byName["docs"].Headers["Authorization"] != "DOCS_AUTHORIZATION" {
		t.Errorf("docs: %+v", byName["docs"])
	}
	for _, secret := range []string{"api-xyz", "Bearer abc"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q was copied into the output", secret)
		}
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"looks like a secret", "path on your computer", "extras.mise", "renamed", "literal value", "not an MCP server"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning containing %q:\n%s", want, joined)
		}
	}
}

func TestImportMCPJSONAcceptsBareMapAndRejectsGarbage(t *testing.T) {
	if out, _, err := ImportMCPJSON([]byte(`{"x":{"command":"uvx","args":["srv"]}}`)); err != nil || !strings.Contains(out, `name = "x"`) {
		t.Errorf("bare map: %v\n%s", err, out)
	}
	if _, _, err := ImportMCPJSON([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
	if _, _, err := ImportMCPJSON([]byte(`{"mcpServers":{}}`)); err == nil {
		t.Error("an empty server list must be an error")
	}
}
