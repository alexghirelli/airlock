package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/detect"
	"github.com/alexghirelli/airlock/internal/doctor"
	"github.com/alexghirelli/airlock/internal/ignore"
	"github.com/alexghirelli/airlock/internal/sandbox"
)

// secretLimit is how many secret-looking files init lists before saying "and more".
const secretLimit = 10

// initReport is what `airlock init --json` prints.
type initReport struct {
	Path        string          `json:"path"`
	Findings    detect.Findings `json:"findings"`
	Secrets     []string        `json:"secrets,omitempty"`
	Suggestions []string        `json:"suggestions,omitempty"`
	Config      string          `json:"config,omitempty"`
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
	secrets, more, secretErr := findSecrets(root)
	text := detect.Render(f)
	suggestions := detect.Suggestions(f, secrets, more)
	path := filepath.Join(root, config.ProjectConfigName)

	if e.a.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(initReport{Path: path, Findings: f, Secrets: secrets, Suggestions: suggestions, Config: text})
	}

	if secretErr != nil {
		e.errf("airlock: warning: %v; secrets are listed without it", secretErr)
	}
	if f.Empty() && len(secrets) == 0 {
		fmt.Fprintf(e.stdout, "airlock: nothing detected in %s; nothing written\n", root)
		return 0, nil
	}
	fmt.Fprintf(e.stdout, "airlock: detected in %s:\n", root)
	for _, pin := range f.Pinned {
		fmt.Fprintf(e.stdout, "  %s: picked up by mise from %s (nothing to add)\n", pin.Tool, pin.File)
	}
	for _, prop := range append(append([]detect.Proposal{}, f.Mise...), f.Apt...) {
		fmt.Fprintf(e.stdout, "  to add, %s: %s\n", prop.Spec, prop.Why)
	}
	for _, n := range f.Notes {
		fmt.Fprintf(e.stdout, "  note: %s\n", n)
	}
	if len(suggestions) > 0 {
		fmt.Fprintln(e.stdout, "airlock: for your user config (airlock config edit) and .airlockignore:")
		for _, s := range suggestions {
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
		fmt.Fprintf(e.stdout, "airlock: proposal for %s:\n%s", path, text)
		return 1, fmt.Errorf("%s already exists (use --force to overwrite it)", path)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return 1, err
	}
	if err := writeFileAtomic(root, config.ProjectConfigName, []byte(text)); err != nil {
		return 1, err
	}
	fmt.Fprintf(e.stdout, "airlock: wrote %s\n", path)
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
// does not hide, and whether the search stopped at the limit. A .airlockignore
// that cannot be read or parsed is reported as an error; the search then runs
// without it, so nothing is hidden by mistake.
func findSecrets(root string) (secrets []string, more bool, err error) {
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
	secrets = doctor.FindSecrets(root, masked, secretLimit)
	return secrets, len(secrets) >= secretLimit, err
}
