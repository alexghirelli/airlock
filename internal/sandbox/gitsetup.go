package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PrepareHost does the one-time, host-side repository setup: the worktrees
// directory exists and is git-excluded, and .git/hooks exists so it can be
// mounted read-only (otherwise the agent could create it).
func PrepareHost(p *Project, worktreesDir string) (warnings []string, err error) {
	gitDir := filepath.Join(p.Root, ".git")
	st, serr := os.Stat(gitDir)
	isRepo := serr == nil && st.IsDir()

	if isRepo {
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot create .git/hooks: %v", err))
		}
	} else {
		warnings = append(warnings, "the project is not a git repository: .git/hooks and .git/config protection and worktrees are unavailable")
	}

	wt := strings.Trim(filepath.ToSlash(worktreesDir), "/")
	if wt == "" {
		return warnings, nil
	}
	if err := os.MkdirAll(filepath.Join(p.Root, filepath.FromSlash(wt)), 0o755); err != nil {
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
