package detect

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alexghirelli/airlock/internal/inbox"
)

// header and hints open the generated file. The hints are commented dotted keys
// and must stay above the first table: uncommented, they would otherwise land
// under [extras].
const header = `# .airlock.toml: this project's settings for airlock, written by ` + "`airlock init`" + `.
# The project file comes with the repository and is untrusted, so only a few keys
# count here. Everything else belongs in your user config (airlock config edit).

# Limits for this project. They can be lowered here, never raised.
# sandbox.memory = "8g"
# sandbox.cpus = 4
# sandbox.pids = 2048
`

// Render returns the starter .airlock.toml for the findings. It sets only keys
// that the project file may set (projectAllowed in config), each with the reason
// it was proposed. It returns "" when there is nothing to set.
func Render(f Findings) string {
	if len(f.Mise) == 0 && len(f.Apt) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n[extras]\n")
	writeList(&b, "mise", f.Mise)
	writeList(&b, "apt", f.Apt)
	return b.String()
}

func writeList(b *strings.Builder, key string, items []Proposal) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s = [\n", key)
	for _, p := range items {
		// Specs were checked by the scanner's patterns: plain ASCII, so Quote is valid TOML.
		fmt.Fprintf(b, "  %s, # %s\n", strconv.Quote(p.Spec), inbox.Clean(p.Why, maxWhy))
	}
	b.WriteString("]\n")
}

// Suggestions lists what belongs in the user config or in .airlockignore. init
// does not write those files itself. secrets are the unmasked secret-looking
// files, found by the caller, which knows the real project root; more is true
// when the search stopped at its limit.
func Suggestions(f Findings, secrets []string, more bool) []string {
	var out []string
	for _, m := range f.MCP {
		out = append(out, fmt.Sprintf("MCP servers in %s (relative to the project): run `airlock mcp import %s` and add the [[mcp]] entries it prints to your user config", m, m))
	}
	if len(f.Services) > 0 {
		var images []string
		for _, s := range f.Services {
			images = append(images, fmt.Sprintf("%s in %s", s.Image, s.File))
		}
		out = append(out, "services: "+strings.Join(images, ", ")+". airlock has no service containers yet, and the sandbox cannot reach the host or your LAN by default. Run the service outside and open its address with network.allow_cidrs in your user config (a hole in the LAN block)")
	}
	if len(secrets) > 0 {
		var lines []string
		for _, s := range secrets {
			lines = append(lines, IgnoreLine(s))
		}
		tail := ""
		if more {
			tail = " and more"
		}
		out = append(out, "secrets: add these lines to .airlockignore to hide them from the agent (init does not write it): "+strings.Join(lines, ", ")+tail)
	}
	return out
}

// IgnoreLine escapes a project path so that .airlockignore matches it literally:
// glob characters, a leading # or !, and trailing spaces get a backslash.
func IgnoreLine(p string) string {
	var b strings.Builder
	for i, r := range p {
		if (i == 0 && (r == '#' || r == '!')) || strings.ContainsRune(`*?[]\`, r) || (r == ' ' && i == len(p)-1) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
