package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/alexghirelli/airlock/internal/config"
)

// cmdMCP handles `airlock mcp import <file>`: it turns an "mcpServers" JSON
// file (Claude Code and others) into [[mcp]] entries on stdout.
func (e *env) cmdMCP() (int, error) {
	if len(e.a.rest) == 0 || e.a.rest[0] != "import" {
		return 2, errors.New("usage: airlock mcp import <file.json|->  (prints [[mcp]] entries for your config)")
	}
	if len(e.a.rest) != 2 {
		return 2, errors.New("usage: airlock mcp import <file.json|->")
	}
	var data []byte
	var err error
	if e.a.rest[1] == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(e.a.rest[1])
	}
	if err != nil {
		return 1, err
	}
	out, warns, err := config.ImportMCPJSON(data)
	for _, w := range warns {
		fmt.Fprintf(e.stderr, "airlock: warning: %s\n", w)
	}
	if err != nil {
		return 1, err
	}
	_, err = io.WriteString(e.stdout, out)
	if err == nil {
		fmt.Fprintf(e.stderr, "airlock: add the entries above to %s (airlock config edit); fix the warnings first\n", e.userConfigPath())
	}
	return 0, err
}

// cmdRestart stops the sandbox and starts it again, which applies a changed
// configuration. Sessions running in it end.
func (e *env) cmdRestart(ctx context.Context) (int, error) {
	l, err := e.load()
	if err != nil {
		return 2, err
	}
	m, err := e.manager(l)
	if err != nil {
		return 1, err
	}
	if err := m.Stop(ctx); err != nil {
		return 1, err
	}
	return e.cmdStart(ctx)
}
