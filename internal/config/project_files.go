package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ProjectFilesDirName is the folder, next to the user config, that holds the
// per-project settings files.
const ProjectFilesDirName = "projects"

// MaxProjectFileSize caps one project file. A settings file is a few kilobytes.
const MaxProjectFileSize = 256 << 10

// projectFile is a project file: the user config schema plus match. Config is
// embedded, so go-toml decodes its keys into it exactly as it does the user
// config, and match is the only extra key.
type projectFile struct {
	Match any `toml:"match"` // a string or a list of strings: see matchList
	Config
}

// matchList reads a match value: one string, or a list of strings.
func matchList(v any) ([]string, error) {
	switch m := v.(type) {
	case nil:
		return nil, errors.New(`match is required: the project roots this file applies to, such as match = ["~/Sites/mw-core"]`)
	case string:
		return []string{m}, nil
	case []any:
		out := make([]string, 0, len(m))
		for _, e := range m {
			s, ok := e.(string)
			if !ok {
				return nil, errors.New("match must be a string or a list of strings: an element is not a string")
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, errors.New("match is an empty list: name the project roots this file applies to")
		}
		return out, nil
	}
	return nil, errors.New("match must be a string or a list of strings")
}

// cleanMatch turns one match entry into an absolute, clean pattern.
func cleanMatch(m string) (string, error) {
	if strings.TrimSpace(m) == "" {
		return "", errors.New("match contains an empty path")
	}
	p := filepath.Clean(ExpandHome(m))
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("match %q must be an absolute path or start with ~", m)
	}
	if _, err := filepath.Match(p, ""); err != nil {
		return "", fmt.Errorf("match %q: %w", m, err)
	}
	return p, nil
}

// parseProjectFile checks a project file on its own and returns its match list,
// cleaned and absolute. Its settings are not applied here.
func parseProjectFile(data []byte) ([]string, error) {
	var pf projectFile
	if err := decodeStrict(data, &pf); err != nil {
		return nil, err
	}
	raws, err := matchList(pf.Match)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raws))
	for _, m := range raws {
		p, err := cleanMatch(m)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// lenientCouldApply reports whether a file that could not be checked might apply
// to root. Only match is decoded, without strictness, so a file with a bad key
// or value still says which projects it names. It says yes when match cannot be
// decoded at all: then nothing is known about the file.
func lenientCouldApply(data []byte, root string) bool {
	var lm struct {
		Match any `toml:"match"`
	}
	if err := toml.Unmarshal(data, &lm); err != nil {
		return true
	}
	raws, err := matchList(lm.Match)
	if err != nil {
		return true
	}
	for _, m := range raws {
		p, err := cleanMatch(m)
		if err != nil {
			return true
		}
		if matchesRoot([]string{p}, root) {
			return true
		}
	}
	return false
}

// decodeProjectFile merges a project file's settings into cfg with the same rules
// as the user config. match was checked by parseProjectFile.
func decodeProjectFile(data []byte, cfg *Config) error {
	pf := projectFile{Config: *cfg}
	if err := decodeStrict(data, &pf); err != nil {
		return err
	}
	*cfg = pf.Config
	return nil
}

// matchesRoot reports whether one of the match patterns names the project root.
// A * or ? does not cross a path separator, so "~/work/*" matches the projects
// directly in ~/work and no deeper.
func matchesRoot(patterns []string, root string) bool {
	for _, p := range patterns {
		if ok, err := filepath.Match(p, root); err == nil && ok {
			return true
		}
	}
	return false
}

// insideOrEqual reports whether p is root or lies below it. Both are clean and
// absolute, and p is resolved through symlinks already.
func insideOrEqual(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// applyProjectFiles merges every file in dir whose match names the project root,
// in file-name order, so a later file wins. See inspectProjectFile for what a
// file that does not parse does.
func applyProjectFiles(res *Result, dir, root string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	type matched struct {
		path string
		data []byte
	}
	var applies []matched
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, matches, warn, err := inspectProjectFile(path, root)
		if err != nil {
			return err
		}
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		if matches {
			applies = append(applies, matched{path, data})
		}
	}
	for _, m := range applies {
		if err := decodeProjectFile(m.data, &res.Config); err != nil {
			return fmt.Errorf("%s: %w", m.path, err)
		}
		normalize(&res.Config)
		if err := res.Config.Validate(); err != nil {
			return fmt.Errorf("%s:\n%w", m.path, err)
		}
		res.ProjectFiles = append(res.ProjectFiles, m.path)
	}
	return nil
}

// inspectProjectFile says what one file of the projects folder does for root:
//
//   - matches: it parses and names root. Its data is returned to apply.
//   - warn: it is skipped and the warning says so. That is a file that does not
//     parse but whose match is known and names other projects, or an entry that
//     is not a file.
//   - err: it could apply to root, or it cannot be checked at all. airlock stops
//     and names the file. This is the case for an unreadable file, a file over
//     the size cap, a symlink that is refused or does not resolve, and a broken
//     file whose match cannot be read or names this project.
func inspectProjectFile(path, root string) (data []byte, matches bool, warn string, err error) {
	data, skip, err := readProjectFile(path, root)
	if err != nil {
		return nil, false, "", err
	}
	if skip != "" {
		return nil, false, skip, nil
	}
	patterns, perr := parseProjectFile(data)
	if perr != nil {
		if lenientCouldApply(data, root) {
			return nil, false, "", fmt.Errorf("%s: %w (this file may apply to this project, so airlock stops; fix it)", path, perr)
		}
		return nil, false, fmt.Sprintf("%s: skipped, it does not name this project and does not parse: %v", path, perr), nil
	}
	return data, matchesRoot(patterns, root), "", nil
}

// readProjectFile reads one file of the projects folder. A symlink is followed
// only to a regular file the user owns, and only when that file is not inside the
// project itself. skip is set for an entry that is not a file: it is ignored with
// a warning. A returned error means the file cannot be checked.
func readProjectFile(path, root string) (data []byte, skip string, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	readPath := path
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, "", fmt.Errorf("%s: a symlink that does not resolve cannot be checked (%v); fix or remove it", path, err)
		}
		if insideOrEqual(target, filepath.Clean(root)) {
			return nil, "", fmt.Errorf("%s links to %s, inside the project: settings files must live outside the project", path, target)
		}
		tfi, err := os.Stat(target)
		if err != nil || !tfi.Mode().IsRegular() || !ownedByUser(tfi) {
			return nil, "", fmt.Errorf("%s: a symlink is followed only to a regular file you own, and %s is not one; it cannot be checked, so remove the link", path, target)
		}
		fi, readPath = tfi, target
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Sprintf("%s: not a regular file; ignored", path), nil
	}
	if fi.Size() > MaxProjectFileSize {
		return nil, "", fmt.Errorf("%s: larger than %d KiB, so it cannot be checked; keep settings files small", path, MaxProjectFileSize>>10)
	}
	f, err := os.Open(readPath)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, MaxProjectFileSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > MaxProjectFileSize {
		return nil, "", fmt.Errorf("%s: larger than %d KiB, so it cannot be checked; keep settings files small", path, MaxProjectFileSize>>10)
	}
	return data, "", nil
}

// ProjectFileChoice is the file that keeps the settings of the project rooted at
// root: <basename>.toml in projectsDir, or <basename>-<hash>.toml when that name
// already belongs to another project. exists reports whether the file found
// there already matches root. A file there that cannot be checked is an error
// (see inspectProjectFile).
func ProjectFileChoice(projectsDir, root string) (path string, exists bool, err error) {
	root = filepath.Clean(root)
	base := filepath.Base(root)
	if base == "." || base == string(filepath.Separator) {
		return "", false, errors.New("the filesystem root cannot be a project")
	}
	sum := sha256.Sum256([]byte(root))
	names := []string{base + ".toml", base + "-" + hex.EncodeToString(sum[:4]) + ".toml"}
	for _, n := range names {
		p := filepath.Join(projectsDir, n)
		_, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return p, false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("%s: %w", p, err)
		}
		_, matches, _, err := inspectProjectFile(p, root)
		if err != nil {
			return "", false, err
		}
		if matches {
			return p, true, nil
		}
	}
	return "", false, fmt.Errorf("%s and %s belong to other projects; rename one of them to match this project", filepath.Join(projectsDir, names[0]), filepath.Join(projectsDir, names[1]))
}

// ProjectFileTemplate is the skeleton written by `airlock config init --project`
// and `config edit --project` for the project rooted at root. Glob characters in
// the root are escaped, so the match names that folder and nothing else. On
// Windows a backslash is the path separator and cannot escape them, so such a
// root is refused.
func ProjectFileTemplate(root string) (string, error) {
	shown := collapseHome(filepath.Clean(root))
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(shown, "*?[") {
			return "", fmt.Errorf("%s contains glob characters (* ? [), which a match cannot escape on Windows: write match by hand, with a pattern that matches only this folder", shown)
		}
	} else {
		shown = escapeGlob(shown)
	}
	return fmt.Sprintf(projectTemplate, tomlString(shown)), nil
}

// escapeGlob escapes the characters filepath.Match reads as syntax.
func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(s)
}

// CreateProjectFile writes body to a new file at path, mode 0600. It fails when
// the file exists, so a file that appeared meanwhile is never overwritten.
func CreateProjectFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReplaceProjectFile writes body over path through a temporary file in the same
// folder and a rename, so the file is never half written. The mode is 0600.
func ReplaceProjectFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".airlock-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// collapseHome writes a path under the home folder with ~ in front.
func collapseHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, filepath.Clean(home)+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return p
}

const projectTemplate = `# airlock settings for one project. They apply to the project folder named in
# match, after your user config and before <project>/.airlock.toml. You own this
# file and the sandbox cannot reach it, so it may set what a project's own
# .airlock.toml may not: mounts, env, setup, worktree_link.
#
# match: the project root(s) this file applies to, a string or a list. ~ is your
# home folder; * and ? match within one folder name, so "~/work/*" is every project
# directly in ~/work. A wildcard applies these settings to every project that
# lands there, including one you cloned without checking it.
match = [%s]

# The keys are the user config's (airlock config show prints them), for example:
#
# [sandbox]
# worktree_link = ["LocalSettings.php", "vendor", ".env"]   # linked into each new worktree
`
