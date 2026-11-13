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

	"github.com/alexghirelli/airlock/internal/hostgit"
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
	// Main is the root of the main repository when Root is a linked git
	// worktree, and "" otherwise. A worktree is a project of its own, with its
	// own sandbox; only the git directory is shared with the main repository.
	Main string
	// Cwd is the host path of the worktree a shared-sandbox session starts in
	// ("" when it starts at Root). See Settle.
	Cwd string
}

// WorkDir is where sessions start inside the sandbox.
func (p *Project) WorkDir() string {
	if p.Cwd != "" {
		return containerPath(p.Cwd)
	}
	return p.Path
}

// Settle decides which sandbox a linked worktree belongs to, once the
// configuration (which says where worktrees live) is known. A worktree directly
// under the worktrees directory shares the repository's sandbox, and the
// returned project is the repository's with Cwd set to the worktree. One in the
// isolated folder, or anywhere else, keeps a sandbox of its own.
func (p *Project) Settle(worktreesDir string) *Project {
	dir := strings.Trim(filepath.ToSlash(worktreesDir), "/")
	if !p.IsWorktree() || dir == "" {
		return p
	}
	rel, err := filepath.Rel(filepath.Join(p.Main, filepath.FromSlash(dir)), p.Root)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, filepath.Separator) {
		return p // not a direct child: the isolated folder, or somewhere else
	}
	return &Project{Root: p.Main, Path: containerPath(p.Main), Slug: slug(p.Main), Cwd: p.Root}
}

// IsWorktree reports whether the project is a linked worktree.
func (p *Project) IsWorktree() bool { return p.Main != "" }

// RepoRoot is the root of the repository that owns the git directory: the
// main repository for a worktree, the project itself otherwise.
func (p *Project) RepoRoot() string {
	if p.Main != "" {
		return p.Main
	}
	return p.Root
}

// GitDir is the repository's git directory on the host.
func (p *Project) GitDir() string { return filepath.Join(p.RepoRoot(), ".git") }

// FamilySlug is the slug shared by a repository and all its worktrees. It
// names what they share, such as per-project credentials.
func (p *Project) FamilySlug() string {
	if p.Main != "" {
		return slug(p.Main)
	}
	return p.Slug
}

// ContainerName is the name of the project's sandbox.
func (p *Project) ContainerName() string { return ContainerPrefix + p.Slug }

var (
	slugBad   = regexp.MustCompile(`[^a-z0-9_.-]+`)
	slugTrim  = regexp.MustCompile(`^[^a-z0-9]+|[^a-z0-9]+$`)
	winDrive  = regexp.MustCompile(`^([A-Za-z]):[\\/]`)
	errNoRoot = errors.New("refusing to use the filesystem root as a project")
)

// Resolve finds the project for a path: the repository root containing it, the
// worktree root when the path is inside a linked worktree (which has its own
// sandbox), or the directory itself when it is not in a repository.
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
	root, main := findRepoRoot(real)
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
	p := &Project{Root: root, Path: containerPath(root), Slug: slug(root), Main: main}
	if main != "" {
		p.Slug = worktreeSlug(main, root)
	}
	return p, nil
}

// findRepoRoot walks up looking for a .git entry and returns the directory
// that holds it. A .git *file* means a worktree or submodule; for a linked
// worktree the main repository is returned too, because the worktree's git
// directory lives there.
func findRepoRoot(dir string) (root, main string) {
	for d := dir; ; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		if st, err := os.Lstat(g); err == nil {
			if st.IsDir() {
				return d, ""
			}
			return d, mainRepoOfWorktree(g)
		}
		if filepath.Dir(d) == d {
			return "", ""
		}
	}
}

// mainRepoOfWorktree returns the main checkout of the linked worktree whose
// ".git" file is gitFile, or "". The file is written by the agent, so the answer
// is only what hostgit can verify: see hostgit.MainOfWorktree.
func mainRepoOfWorktree(gitFile string) string {
	return hostgit.MainOfWorktree(filepath.Dir(gitFile))
}

func slug(root string) string { return slugOf(filepath.Base(root), root) }

// worktreeSlug names a worktree's sandbox after the repository and the
// worktree, so `airlock ls` shows which is which.
func worktreeSlug(main, root string) string {
	return slugOf(filepath.Base(main)+"-"+filepath.Base(root), root)
}

func slugOf(label, root string) string {
	name := strings.ToLower(label)
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
