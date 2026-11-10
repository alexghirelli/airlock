// Package sandbox turns a project and a configuration into a running sandbox.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ContainerPrefix starts the name of every sandbox container.
const ContainerPrefix = "airlock-"

// Project is a directory that gets its own sandbox.
type Project struct {
	// Root is the absolute, symlink-free path on the host.
	Root string
	// Path is where the project appears inside the sandbox. It equals Root on
	// Unix hosts, so git metadata (worktrees store absolute paths) is valid on
	// both sides.
	Path string
	// Slug is a sanitized directory name plus a short hash of Root, so two
	// projects with the same directory name never collide.
	Slug string
}

// ContainerName is the name of the project's sandbox.
func (p *Project) ContainerName() string { return ContainerPrefix + p.Slug }

var (
	slugBad   = regexp.MustCompile(`[^a-z0-9_.-]+`)
	slugTrim  = regexp.MustCompile(`^[^a-z0-9]+|[^a-z0-9]+$`)
	winDrive  = regexp.MustCompile(`^([A-Za-z]):[\\/]`)
	errNoRoot = errors.New("refusing to use the filesystem root as a project")
)

// Resolve finds the project for a path: the repository root containing it
// (the main repository when the path is inside a worktree), or the directory
// itself when it is not in a repository.
func Resolve(path string) (*Project, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("project path: %w", err)
	}
	st, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", real)
	}
	root := findRepoRoot(real)
	if root == "" {
		root = real
	}
	if filepath.Dir(root) == root {
		return nil, errNoRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil && root == h {
			return nil, errors.New("refusing to use your home directory as a project: it would hand the agent everything in it; run airlock inside a project folder")
		}
	}
	return &Project{Root: root, Path: containerPath(root), Slug: slug(root)}, nil
}

// findRepoRoot walks up looking for a .git entry. A .git *file* means a
// worktree or submodule; for a worktree of a repository the main repository is
// returned, because the worktree's git directory lives there.
func findRepoRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		if st, err := os.Lstat(g); err == nil {
			if st.IsDir() {
				return d
			}
			if main := mainRepoOfWorktree(g); main != "" {
				return main
			}
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// mainRepoOfWorktree reads a ".git" file ("gitdir: <repo>/.git/worktrees/<n>")
// and returns <repo>, or "" if it is not a linked worktree.
func mainRepoOfWorktree(gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	gitdir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	gitdir = filepath.ToSlash(strings.TrimSpace(gitdir))
	if !filepath.IsAbs(filepath.FromSlash(gitdir)) {
		gitdir = filepath.ToSlash(filepath.Join(filepath.Dir(gitFile), filepath.FromSlash(gitdir)))
	}
	i := strings.LastIndex(gitdir, "/.git/worktrees/")
	if i < 0 {
		return ""
	}
	return filepath.FromSlash(gitdir[:i])
}

func slug(root string) string {
	name := strings.ToLower(filepath.Base(root))
	name = slugBad.ReplaceAllString(name, "-")
	name = slugTrim.ReplaceAllString(name, "")
	if len(name) > 24 {
		name = slugTrim.ReplaceAllString(name[:24], "")
	}
	if name == "" {
		name = "project"
	}
	key := root
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(root) // these filesystems are case-insensitive by default
	}
	sum := sha256.Sum256([]byte(key))
	return name + "-" + hex.EncodeToString(sum[:])[:8]
}

// containerPath maps a host path to its POSIX path inside the sandbox. On
// Windows, C:\Users\me\proj becomes /c/Users/me/proj; worktrees created inside
// are then usable from WSL, not from native Windows git (see docs/platforms.md).
func containerPath(hostPath string) string {
	if m := winDrive.FindStringSubmatch(hostPath); m != nil {
		return "/" + strings.ToLower(m[1]) + "/" + strings.ReplaceAll(hostPath[3:], `\`, "/")
	}
	return filepath.ToSlash(hostPath)
}
