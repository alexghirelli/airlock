package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexghirelli/airlock/internal/worktree"
)

// PrepareHost does the one-time, host-side repository setup: the worktrees
// directory exists and is git-excluded (it is hidden from the project's own
// sandbox, see BuildPlan), and .git/hooks exists so it can be mounted
// read-only (otherwise the agent could create it).
func PrepareHost(p *Project, worktreesDir string) (warnings []string, err error) {
	gitDir := p.GitDir()
	st, serr := os.Stat(gitDir)
	isRepo := serr == nil && st.IsDir()

	if isRepo {
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot create .git/hooks: %v", err))
		}
	} else {
		warnings = append(warnings, "the project is not a git repository: .git/hooks and .git/config protection and worktrees are unavailable")
	}

	if isRepo && p.IsWorktree() {
		// Make sure the worktree's own config file exists, so that it can be
		// mounted read-only (git reads it when extensions.worktreeConfig is on).
		if admin := worktree.AdminDir(p.Root); admin != "" {
			f := filepath.Join(admin, "config.worktree")
			if !fileExists(f) {
				if err := os.WriteFile(f, nil, 0o644); err != nil {
					warnings = append(warnings, fmt.Sprintf("cannot create %s: %v", f, err))
				}
			}
		}
	}

	wt := strings.Trim(filepath.ToSlash(worktreesDir), "/")
	if wt == "" || p.IsWorktree() {
		return warnings, nil
	}
	// The isolated folder must exist: the project's sandbox hides it.
	if err := os.MkdirAll(filepath.Join(p.Root, filepath.FromSlash(wt), worktree.IsolatedDir), 0o755); err != nil {
		return warnings, fmt.Errorf("create %s: %w", wt, err)
	}
	if isRepo {
		if err := excludeFromGit(gitDir, "/"+wt+"/"); err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot add /%s/ to .git/info/exclude: %v", wt, err))
		}
	}
	return warnings, nil
}

// excludeFromGit appends a pattern to .git/info/exclude unless it is there.
func excludeFromGit(gitDir, pattern string) error {
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(infoDir, "exclude")
	existing, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	add := pattern + "\n"
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		add = "\n" + add
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(add)
	return err
}

func fileExists(f string) bool {
	st, err := os.Stat(f)
	return err == nil && st.Mode().IsRegular()
}
