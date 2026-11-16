package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/detect"
	"github.com/alexghirelli/airlock/internal/doctor"
	"github.com/alexghirelli/airlock/internal/ignore"
	"github.com/alexghirelli/airlock/internal/inbox"
	"github.com/alexghirelli/airlock/internal/sandbox"
)

// secretLimit is how many secret-looking files init lists before saying "and more".
const secretLimit = 10

// maxField caps one value taken from the repository in the report. maxSentence
// caps a suggestion or note, which is a sentence built around such values.
const (
	maxField    = 512
	maxSentence = 4096
)

// initReport is what `airlock init --json` prints.
type initReport struct {
	Path        string          `json:"path"`
	Findings    detect.Findings `json:"findings"`
	Secrets     []string        `json:"secrets,omitempty"`
	Suggestions []string        `json:"suggestions,omitempty"`
	Config      string          `json:"config,omitempty"`
}

// cleanReport is the one place where what init prints is made safe. Almost all
// of it comes from the repository (file and folder names, image names, version
// strings, reasons, secret paths), and any of it could carry escape sequences,
// bidi overrides or line breaks. Both the text and the JSON output print the
// result, never the raw report.
func cleanReport(r initReport) initReport {
	field := func(s string) string { return inbox.Clean(s, maxField) }
	sentence := func(s string) string { return inbox.Clean(s, maxSentence) }
	props := func(in []detect.Proposal) []detect.Proposal {
		if in == nil {
			return nil
		}
		out := make([]detect.Proposal, len(in))
		for i, p := range in {
			out[i] = detect.Proposal{Spec: field(p.Spec), From: field(p.From), Why: field(p.Why)}
		}
		return out
	}
	f := detect.Findings{
		Mise:   props(r.Findings.Mise),
		Apt:    props(r.Findings.Apt),
		MCP:    mapStrings(r.Findings.MCP, field),
		Agents: mapStrings(r.Findings.Agents, field),
		Notes:  mapStrings(r.Findings.Notes, sentence),
	}
	for _, p := range r.Findings.Pinned {
		f.Pinned = append(f.Pinned, detect.Pin{Tool: field(p.Tool), File: field(p.File)})
	}
	for _, s := range r.Findings.Services {
		f.Services = append(f.Services, detect.Service{File: field(s.File), Image: field(s.Image)})
	}
	return initReport{
		Path:        inbox.Clean(r.Path, 0),
		Findings:    f,
		Secrets:     mapStrings(r.Secrets, field),
		Suggestions: mapStrings(r.Suggestions, sentence),
		Config:      cleanBlock(r.Config),
	}
}

// mapStrings applies clean to each string, keeping nil as nil.
func mapStrings(in []string, clean func(string) string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = clean(s)
	}
	return out
}

// cleanBlock cleans a text of several lines, such as the proposed config. Each
// line is cleaned on its own, so a line break in a value cannot run into the
// next line, and the indentation of each line is kept.
func cleanBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		rest := strings.TrimLeft(l, " \t")
		indent := l[:len(l)-len(rest)]
		lines[i] = indent + inbox.Clean(rest, 0)
	}
	return strings.Join(lines, "\n")
}

// cmdInit writes a starter .airlock.toml for a project from what it uses, and
// says what belongs in the user config instead. The project is the repository
// root, also when run inside a worktree. --json only shows the result.
func (e *env) cmdInit() (int, error) {
	p, err := sandbox.Resolve(e.a.path)
	if err != nil {
		return 2, err
	}
	root := p.RepoRoot()
	f := detect.Scan(os.DirFS(root))
	secrets, more, skipped, secretErr := findSecrets(root)
	suggestions := detect.Suggestions(f, secrets, more)
	if skipped > 0 {
		suggestions = append(suggestions, fmt.Sprintf(
			"secrets: %d file(s) with unusual names skipped; they are not listed (rename them, or add them to .airlockignore by hand)", skipped))
	}
	path := filepath.Join(root, config.ProjectConfigName)
	rep := cleanReport(initReport{Path: path, Findings: f, Secrets: secrets, Suggestions: suggestions, Config: detect.Render(f)})
	text := rep.Config
	shown := rep.Path

	if e.a.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(rep)
	}

	if secretErr != nil {
		e.errf("airlock: warning: %s; secrets are listed without it", inbox.Clean(secretErr.Error(), maxField))
	}
	if f.Empty() && len(secrets) == 0 && skipped == 0 {
		fmt.Fprintf(e.stdout, "airlock: nothing detected in %s; nothing written\n", shown)
		return 0, nil
	}
	fmt.Fprintf(e.stdout, "airlock: detected in %s:\n", inbox.Clean(root, 0))
	for _, pin := range rep.Findings.Pinned {
		fmt.Fprintf(e.stdout, "  %s: picked up by mise from %s (nothing to add)\n", pin.Tool, pin.File)
	}
	for _, prop := range append(append([]detect.Proposal{}, rep.Findings.Mise...), rep.Findings.Apt...) {
		fmt.Fprintf(e.stdout, "  to add, %s: %s\n", prop.Spec, prop.Why)
	}
	for _, n := range rep.Findings.Notes {
		fmt.Fprintf(e.stdout, "  note: %s\n", n)
	}
	if len(rep.Suggestions) > 0 {
		fmt.Fprintln(e.stdout, "airlock: for your user config (airlock config edit) and .airlockignore:")
		for _, s := range rep.Suggestions {
			fmt.Fprintf(e.stdout, "  - %s\n", s)
		}
	}

	if text == "" {
		fmt.Fprintln(e.stdout, "airlock: nothing to set in .airlock.toml; nothing written")
		fmt.Fprintln(e.stdout, "airlock: next: airlock")
		return 0, nil
	}
	// The write step is the only place that refuses. A symlink or any other
	// non-file is refused even with --force: it would point the write elsewhere.
	st, err := os.Lstat(path)
	switch {
	case err == nil && !st.Mode().IsRegular():
		return 1, fmt.Errorf("%s is not a regular file (a link or a folder); not writing through it, even with --force", path)
	case err == nil && !e.a.force:
		fmt.Fprintf(e.stdout, "airlock: proposal for %s:\n%s", shown, text)
		return 1, fmt.Errorf("%s already exists (use --force to overwrite it)", path)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return 1, err
	}
	if err := writeFileAtomic(root, config.ProjectConfigName, []byte(text)); err != nil {
		return 1, err
	}
	fmt.Fprintf(e.stdout, "airlock: wrote %s\n", shown)
	fmt.Fprintln(e.stdout, "airlock: next: airlock")
	return 0, nil
}

// writeFileAtomic writes dir/name through a temporary file and a rename, so the
// directory entry is replaced and never a file it might point to. Mode 0644.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	done = true
	return nil
}

// findSecrets lists up to secretLimit secret-looking files that .airlockignore
// does not hide, and whether the search stopped at the limit. A name with a
// control character, a line break or a format character is not listed: it is
// counted in skipped, because a line break would split the .airlockignore line
// it is suggested on. A .airlockignore that cannot be read or parsed is reported
// as an error; the search then runs without it, so nothing is hidden by mistake.
func findSecrets(root string) (secrets []string, more bool, skipped int, err error) {
	var masked *ignore.Matcher
	path := filepath.Join(root, sandbox.IgnoreFile)
	if f, openErr := os.Open(path); openErr == nil {
		m, parseErr := ignore.New(f)
		f.Close()
		if parseErr != nil {
			err = fmt.Errorf("%s: %w", sandbox.IgnoreFile, parseErr)
		} else {
			masked = m
		}
	} else if !errors.Is(openErr, fs.ErrNotExist) {
		err = fmt.Errorf("%s: %w", sandbox.IgnoreFile, openErr)
	}
	found := doctor.FindSecrets(root, masked, secretLimit)
	for _, name := range found {
		if detect.Unusual(name) {
			skipped++
			continue
		}
		secrets = append(secrets, name)
	}
	return secrets, len(found) >= secretLimit, skipped, err
}
