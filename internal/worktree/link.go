package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alexghirelli/airlock/internal/hostgit"
)

// Link is one symlink made in a worktree.
type Link struct {
	Path   string // slash-separated, relative to the worktree
	Target string // as written in the link: relative to the link's folder
}

// LinkResult says what a Linker did in one worktree.
type LinkResult struct {
	Linked  []Link   // links made now
	Already int      // links that were already there, pointing where they should
	Present int      // paths the worktree already has, which are not links: left alone
	Tracked int      // paths git tracks (in the project or in the worktree's branch): left alone
	Skipped []string // anything else left alone, one line each
}

// Linker makes the links of sandbox.worktree_link in the worktrees of one
// repository. The project's tracked files are read once, not per worktree.
type Linker struct {
	repo     *Repo
	patterns []string
	main     string          // the main checkout, resolved
	folders  []string        // folder names never linked
	tracked  map[string]bool // what git tracks in the main checkout, read on first use
}

// Linker returns the linker for patterns in this repository.
func (r *Repo) Linker(patterns []string) (*Linker, error) {
	main, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		return nil, err
	}
	folders := []string{".git", ".worktrees"}
	if f := firstFolder(r.Dir); f != "" && f != ".worktrees" {
		folders = append(folders, f)
	}
	return &Linker{repo: r, patterns: patterns, main: main, folders: folders}, nil
}

// Link makes, in the worktree at wtPath, a relative symlink for every path of
// the main checkout that one of the patterns matches, when the worktree does not
// have that path and git does not track it. Nothing that exists is replaced or
// removed. The worktree folder is written by the agent, so every change goes
// through an os.Root: no part of a destination can be a link out of the
// worktree, and a folder of the destination that is a link is skipped rather than
// followed, since the relative target would then be wrong.
func (lk *Linker) Link(wtPath string, isolated bool) (LinkResult, error) {
	var res LinkResult
	if len(lk.patterns) == 0 {
		return res, nil
	}
	if isolated {
		res.Skipped = append(res.Skipped, "the worktree has a sandbox of its own, which does not mount the main checkout: the links would dangle")
		return res, nil
	}
	main := lk.main
	wt, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		return res, err
	}
	if wt == main {
		return res, fmt.Errorf("%s is the main checkout, not a worktree", wt)
	}
	if _, err := hostgit.Open(main, wt); err != nil {
		return res, fmt.Errorf("%s is not a worktree of %s: %w", wt, main, err)
	}
	wtRoot, err := os.OpenRoot(wt)
	if err != nil {
		return res, err
	}
	defer wtRoot.Close()
	// The folder opened must be the folder at wt, and a folder. The agent can
	// replace it between the two calls.
	before, err := os.Lstat(wt)
	if err != nil {
		return res, err
	}
	opened, err := wtRoot.Stat(".")
	if err != nil {
		return res, err
	}
	if !before.IsDir() || !opened.IsDir() || !os.SameFile(before, opened) {
		return res, fmt.Errorf("%s changed while airlock was opening it; not linking", wt)
	}

	cands, skipped := matchPaths(main, lk.patterns, lk.folders)
	res.Skipped = append(res.Skipped, skipped...)
	if len(cands) == 0 {
		return res, nil
	}
	if err := lk.readTracked(); err != nil {
		return res, err
	}
	trackedWT, err := lsTracked(main, wt)
	if err != nil {
		return res, err
	}

	var held []string // links that are in the worktree now, made or already there
	for _, rel := range cands {
		name := filepath.FromSlash(rel)
		if l, ok := linkedAbove(held, rel); ok {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: inside %s, which is linked", rel, l))
			continue
		}
		entry := filepath.Join(main, name)
		real, err := filepath.EvalSymlinks(entry)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: does not resolve (%v)", rel, err))
			continue
		}
		if !insideOrEqual(real, main) {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: a link that leads outside the project", rel))
			continue
		}
		if lk.tracked[rel] || trackedWT[rel] {
			res.Tracked++
			continue
		}
		dir := filepath.Dir(name)
		target, err := filepath.Rel(filepath.Join(wt, dir), entry)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: no relative path to the main checkout (%v)", rel, err))
			continue
		}
		if msg := ensureRealDirs(wtRoot, dir); msg != "" {
			res.Skipped = append(res.Skipped, rel+": "+msg)
			continue
		}
		fi, err := wtRoot.Lstat(name)
		switch {
		case err == nil:
			if fi.Mode()&fs.ModeSymlink != 0 {
				if got, rerr := wtRoot.Readlink(name); rerr == nil && got == target {
					res.Already++
					held = append(held, rel)
					continue
				}
			}
			res.Present++
			continue
		case !errors.Is(err, fs.ErrNotExist):
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: cannot look at the worktree (%v)", rel, err))
			continue
		}
		if err := wtRoot.Symlink(target, name); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		res.Linked = append(res.Linked, Link{Path: rel, Target: target})
		held = append(held, rel)
	}
	return res, nil
}

// readTracked reads what git tracks in the main checkout, once.
func (lk *Linker) readTracked() error {
	if lk.tracked != nil {
		return nil
	}
	t, err := lsTracked(lk.main, "")
	if err != nil {
		return err
	}
	lk.tracked = t
	return nil
}

// Link makes the links of patterns in one worktree (see Linker.Link).
func (r *Repo) Link(wtPath string, isolated bool, patterns []string) (LinkResult, error) {
	lk, err := r.Linker(patterns)
	if err != nil {
		return LinkResult{}, err
	}
	return lk.Link(wtPath, isolated)
}

// matchPaths expands the patterns against the main checkout, relative to its root,
// as filepath.Glob would. It returns the matches once each, sorted, and one line
// for each pattern that matches nothing or cannot be used.
//
// The matching is done here, one path element at a time, and not with
// filepath.Glob, which would read the project's own path as a pattern when it
// holds a [ or a *. It sees a link as a match of its own: a link that leads
// outside the project is then reported by Link, not mistaken for a missing file.
func matchPaths(root string, patterns, folders []string) (cands, skipped []string) {
	seen := map[string]bool{}
	for _, p := range patterns {
		matches, err := expandPattern(root, path.Clean(filepath.ToSlash(p)))
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		if len(matches) == 0 {
			skipped = append(skipped, p+": matches nothing in the project")
			continue
		}
		for _, m := range matches {
			if m == "." || seen[m] {
				continue
			}
			seen[m] = true
			if inFolders(m, folders) {
				skipped = append(skipped, m+": inside git's folder or the worktrees folder, which are never linked")
				continue
			}
			cands = append(cands, m)
		}
	}
	sort.Strings(cands)
	return cands, skipped
}

// expandPattern returns the slash-separated paths under root that a clean relative
// pattern matches, each element with path.Match. Missing literal elements are
// simply not matches.
func expandPattern(root, pattern string) ([]string, error) {
	cur := []string{""}
	for _, part := range strings.Split(pattern, "/") {
		if _, err := path.Match(part, ""); err != nil {
			return nil, err
		}
		var next []string
		if !strings.ContainsAny(part, "*?[\\") {
			for _, base := range cur {
				rel := joinSlash(base, part)
				if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
					next = append(next, rel)
				}
			}
		} else {
			for _, base := range cur {
				ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(base)))
				if err != nil {
					continue
				}
				for _, e := range ents {
					if ok, _ := path.Match(part, e.Name()); ok {
						next = append(next, joinSlash(base, e.Name()))
					}
				}
			}
		}
		cur = next
	}
	return cur, nil
}

func joinSlash(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}

// inFolders reports whether a slash path has one of the folder names as a part.
func inFolders(rel string, folders []string) bool {
	for _, part := range strings.Split(rel, "/") {
		for _, f := range folders {
			if part == f {
				return true
			}
		}
	}
	return false
}

// insideOrEqual reports whether p is root or lies below it. Both are absolute and
// clean.
func insideOrEqual(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// linkedAbove returns the link whose path is a folder of rel, if there is one.
func linkedAbove(linked []string, rel string) (string, bool) {
	for _, l := range linked {
		if strings.HasPrefix(rel, l+"/") {
			return l, true
		}
	}
	return "", false
}

// firstFolder is the first folder of a relative folder path, "" for none.
func firstFolder(dir string) string {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	first, _, _ := strings.Cut(dir, "/")
	return first
}

// ensureRealDirs makes the folder dir inside the worktree, and returns a reason
// when it cannot: a part of the path is a link (its target would not be the one
// the relative link was computed for) or leads outside the worktree. Missing
// parts are made as folders.
func ensureRealDirs(wtRoot *os.Root, dir string) string {
	if dir == "." {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(dir), "/")
	cur := ""
	for _, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		fi, err := wtRoot.Lstat(filepath.FromSlash(cur))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := wtRoot.Mkdir(filepath.FromSlash(cur), 0o755); err != nil {
				return fmt.Sprintf("cannot make folder %s in the worktree (%v)", cur, err)
			}
		case err != nil:
			return fmt.Sprintf("cannot look at folder %s in the worktree (%v)", cur, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Sprintf("folder %s of the worktree is a link: not followed", cur)
		case !fi.IsDir():
			return fmt.Sprintf("%s in the worktree is not a folder", cur)
		}
	}
	return ""
}

// lsTracked lists what git tracks in the checkout at dir (the main checkout when
// dir is ""): every file and every folder above one, slash-separated.
func lsTracked(root, dir string) (map[string]bool, error) {
	loc, err := hostgit.Open(root, dir)
	if err != nil {
		return nil, err
	}
	out, err := loc.Output(context.Background(), hostgit.Options{Timeout: 2 * time.Minute, MaxOutput: 512 << 20}, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		for p := f; p != "" && p != "." && !set[p]; p = path.Dir(p) {
			set[p] = true
		}
	}
	return set, nil
}
