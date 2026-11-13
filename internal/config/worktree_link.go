package config

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// WorktreeLinkName is the names of the sandbox.worktree_link key, as Validate and
// the messages say it.
const WorktreeLinkName = "sandbox.worktree_link"

// validLinkPattern checks one sandbox.worktree_link pattern. A pattern is relative
// to the project root, uses / as its separator, and may not name git's own
// folder, the worktrees folder (.worktrees, or the first folder of
// worktreesDir when that is another name), the root itself, or anything outside
// the root.
func validLinkPattern(p, worktreesDir string) error {
	wtFirst := firstFolder(worktreesDir)
	switch {
	case strings.TrimSpace(p) == "":
		return errors.New("a pattern is empty")
	case strings.ContainsAny(p, "\x00\n\r\t"):
		return errors.New("a pattern contains control characters")
	case strings.Contains(p, `\`):
		return errors.New("use / as the separator in " + p)
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~"):
		return errors.New(p + " must be relative to the project root")
	}
	clean := path.Clean(p)
	for _, part := range strings.Split(p, "/") {
		switch {
		case part == "..":
			return errors.New(p + " must not contain ..")
		case part == ".git":
			return errors.New(p + " must not link git's own folder (.git)")
		case part == ".worktrees" || (wtFirst != "" && part == wtFirst):
			return errors.New(p + " must not link the worktrees folder (" + part + ")")
		}
	}
	if clean == "." {
		return errors.New(p + " names the project root itself")
	}
	if _, err := filepath.Match(clean, ""); err != nil {
		return errors.New(p + ": " + err.Error())
	}
	return nil
}

// firstFolder is the first folder of a relative folder path, "" for none.
func firstFolder(dir string) string {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	first, _, _ := strings.Cut(dir, "/")
	return first
}
