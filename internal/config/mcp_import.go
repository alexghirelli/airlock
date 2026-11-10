package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	secretNameRe  = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|credential|auth)`)
	badNameCharRe = regexp.MustCompile(`[^a-z0-9_-]+`)
	nonEnvCharRe  = regexp.MustCompile(`[^A-Z0-9]+`)
	hostPathRe    = regexp.MustCompile(`^(/Users/|/home/|[A-Za-z]:[\\/])|/\.?venv/|/node_modules/`)
)

// ImportMCPJSON converts the "mcpServers" JSON used by Claude Code and many
// other tools into airlock [[mcp]] entries. It returns the TOML and warnings
// about everything that will not work as written inside the sandbox. Values
// that look like secrets are never copied: they become references to host
// environment variables.
func ImportMCPJSON(data []byte) (string, []string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return "", nil, fmt.Errorf("not valid JSON: %w", err)
	}
	servers := top
	if raw, ok := top["mcpServers"]; ok {
		servers = nil
		if err := json.Unmarshal(raw, &servers); err != nil {
			return "", nil, fmt.Errorf("mcpServers: %w", err)
		}
	}

	var out bytes.Buffer
	var warns []string
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	count := 0
	for _, orig := range names {
		var s struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(servers[orig], &s); err != nil || (s.Command == "" && s.URL == "") {
			warns = append(warns, fmt.Sprintf("%s: not an MCP server entry (no command or url); skipped", orig))
			continue
		}
		name := strings.Trim(badNameCharRe.ReplaceAllString(strings.ToLower(orig), "-"), "-_")
		if len(name) > 32 {
			name = name[:32]
		}
		if name == "" {
			warns = append(warns, fmt.Sprintf("%s: cannot be turned into a valid name; skipped", orig))
			continue
		}
		if name != orig {
			warns = append(warns, fmt.Sprintf("%s: renamed to %q (names are lowercase letters, digits, - and _)", orig, name))
		}
		warn := func(format string, a ...any) { warns = append(warns, name+": "+fmt.Sprintf(format, a...)) }

		count++
		fmt.Fprintf(&out, "[[mcp]]\nname = %s\n", tomlString(name))
		if s.Command != "" {
			fmt.Fprintf(&out, "command = %s\n", tomlString(s.Command))
			if len(s.Args) > 0 {
				fmt.Fprintf(&out, "args = %s\n", tomlArray(s.Args))
			}
			literal, fromHost := map[string]string{}, map[string]string{}
			for k, v := range s.Env {
				if secretNameRe.MatchString(k) {
					fromHost[k] = k
					warn("env %s looks like a secret and was not copied; export %s on your computer and airlock passes it in", k, k)
				} else {
					literal[k] = v
				}
			}
			if len(literal) > 0 {
				fmt.Fprintf(&out, "env = %s\n", tomlInline(literal))
			}
			if len(fromHost) > 0 {
				fmt.Fprintf(&out, "env_from_host = %s\n", tomlInline(fromHost))
			}
			if hint := runtimeHint(s.Command); hint != "" {
				warn("runs %q, which the sandbox only has if you add %s to extras.mise", s.Command, hint)
			}
			for _, p := range append([]string{s.Command}, s.Args...) {
				if hostPathRe.MatchString(p) {
					warn("%q is a path on your computer. Mount that folder with [[mount]] and use /mnt/<name>/... instead; a virtualenv or node_modules built on your computer will not run in the Linux sandbox, so create it with a [[setup]] step", p)
					break
				}
			}
		} else {
			fmt.Fprintf(&out, "url = %s\n", tomlString(s.URL))
			hdr := map[string]string{}
			for h := range s.Headers {
				v := nonEnvCharRe.ReplaceAllString(strings.ToUpper(name+"_"+h), "_")
				hdr[h] = v
				warn("header %s has a literal value and was not copied; export %s on your computer with that value", h, v)
			}
			if len(hdr) > 0 {
				fmt.Fprintf(&out, "headers = %s\n", tomlInline(hdr))
			}
		}
		out.WriteString("\n")
	}
	if count == 0 {
		return "", warns, fmt.Errorf("no MCP servers found")
	}
	return out.String(), warns, nil
}

func runtimeHint(command string) string {
	if strings.ContainsAny(command, `/\`) {
		return "" // a path, not a program looked up on PATH
	}
	switch path.Base(command) {
	case "node", "npx", "npm":
		return `"node@22"`
	case "python", "python3", "pip", "pip3":
		return `"python@3.12"`
	case "uv", "uvx":
		return `"uv"`
	}
	return ""
}

func tomlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSpace(b.String())
}

func tomlArray(a []string) string {
	parts := make([]string, len(a))
	for i, s := range a {
		parts[i] = tomlString(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tomlInline(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = tomlString(k) + " = " + tomlString(m[k])
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
