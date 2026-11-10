package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"airlock/internal/backend"
	"airlock/internal/ignore"
)

// IgnoreFile lists, in gitignore syntax, the project files to hide from the
// sandbox. Matching files appear empty (and read-only) inside it.
const IgnoreFile = ".airlockignore"

const maxMaskWalk = 200000

// StateDir is where airlock keeps host-side state (mask sources, audit baselines).
func StateDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "airlock")
	}
	return filepath.Join(os.TempDir(), "airlock")
}

// ignoreMasks returns the binds that hide files matching the project's
// .airlockignore. Masks are decided when the sandbox is created: files that
// appear later are not hidden until it is recreated.
func ignoreMasks(p *Project) ([]backend.Bind, []string) {
	f, err := os.Open(filepath.Join(p.Root, IgnoreFile))
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	m, err := ignore.New(f)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", IgnoreFile, err)}
	}

	var warns []string
	emptyFile, emptyDir, err := maskSources()
	if err != nil {
		return nil, []string{fmt.Sprintf("cannot prepare file masks: %v", err)}
	}

	var binds []backend.Bind
	seen := 0
	err = filepath.WalkDir(p.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are simply not masked
		}
		rel, _ := filepath.Rel(p.Root, abs)
		if rel == "." {
			return nil
		}
		if d.IsDir() && (rel == ".git" || rel == ".worktrees") {
			return filepath.SkipDir
		}
		if seen++; seen > maxMaskWalk {
			warns = append(warns, fmt.Sprintf("%s: stopped after %d entries; remaining files are not masked", IgnoreFile, maxMaskWalk))
			return filepath.SkipAll
		}
		slashed := filepath.ToSlash(rel)
		if !m.Match(slashed, d.IsDir()) {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			warns = append(warns, fmt.Sprintf("%s: %s is a symbolic link and cannot be masked", IgnoreFile, slashed))
		case d.IsDir():
			binds = append(binds, backend.Bind{Source: emptyDir, Target: containerPath(abs), ReadOnly: true})
			return filepath.SkipDir // everything inside is hidden with it
		case d.Type().IsRegular():
			binds = append(binds, backend.Bind{Source: emptyFile, Target: containerPath(abs), ReadOnly: true})
		}
		return nil
	})
	if err != nil {
		warns = append(warns, fmt.Sprintf("%s: %v", IgnoreFile, err))
	}
	return binds, warns
}

// maskSources returns an empty file and an empty directory that are bind-mounted
// over hidden paths.
func maskSources() (file, dir string, err error) {
	base := filepath.Join(StateDir(), "mask")
	file, dir = filepath.Join(base, "empty-file"), filepath.Join(base, "empty-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(file, nil, 0o444); err != nil && !strings.Contains(err.Error(), "permission denied") {
		return "", "", err
	}
	if _, err := os.Stat(file); err != nil {
		return "", "", err
	}
	return file, dir, nil
}
