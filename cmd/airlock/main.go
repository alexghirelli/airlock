// Command airlock starts a hardened, per-project sandbox for AI coding agents.
package main

import (
	"os"

	"github.com/alexghirelli/airlock/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
