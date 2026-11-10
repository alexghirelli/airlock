// Package audit reports changes made inside the sandbox to files that run, or
// are loaded automatically, on the host: git hooks, editor and shell
// configuration, build and CI definitions, new executables. It reports; it
// never reverts anything.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"airlock/internal/ignore"
)

// WatchedPatterns are the default gitignore-style patterns of files whose
// change deserves a look, relative to the project root.
var WatchedPatterns = []string{
	"/.git/hooks/", "/.git/config",
	".envrc", ".env.rc", ".tool-versions", "mise.toml", ".mise.toml", ".mise/",
	".vscode/", ".idea/", ".devcontainer/", ".zed/",
	"Makefile", "makefile", "GNUmakefile", "justfile", "Justfile", "Taskfile.yml", "Taskfile.yaml",
	"package.json", "composer.json", ".npmrc", ".yarnrc", ".yarnrc.yml", ".husky/", ".pre-commit-config.yaml",
	".github/workflows/", ".github/actions/", ".gitlab-ci.yml", ".circleci/", "Jenkinsfile", "azure-pipelines.yml", ".buildkite/",
	"flake.nix", "shell.nix", "direnv.toml",
	"/.airlock.toml", "/.airlock/", "/.airlockignore",
}

// skipDirs are not walked: huge, generated, or not the project's own files.
var skipDirs = map[string]bool{".git": true, ".worktrees": true, "node_modules": true, ".venv": true}

const (
	maxWalk     = 300000
	maxFileSize = 4 << 20
)

// Entry is the recorded state of one file.
type Entry struct {
	Hash       string            `json:"hash"`
	Executable bool              `json:"exec,omitempty"`
	Scripts    map[string]string `json:"scripts,omitempty"` // package.json / composer.json
	Watched    bool              `json:"watched,omitempty"`
}

// Snapshot is the state of all watched and executable files at one time.
type Snapshot struct {
	Taken time.Time        `json:"taken"`
	Files map[string]Entry `json:"files"`
}

// Take records the project at root. extra are additional watch patterns.
func Take(root string, extra []string) (*Snapshot, error) {
	watch := ignore.FromLines(append(append([]string(nil), WatchedPatterns...), extra...)...)
	snap := &Snapshot{Taken: time.Now().UTC(), Files: map[string]Entry{}}
	seen := 0
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, abs)
		if rel == "." {
			return nil
		}
		slashed := filepath.ToSlash(rel)
		if d.IsDir() {
			// The one part of .git we care about is hooks (and config, a file).
			if slashed == ".git" {
				return walkGit(root, snap, watch)
			}
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > maxWalk {
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		exec := info.Mode().Perm()&0o111 != 0
		watched := watch.Match(slashed, false)
		if !watched && !exec {
			return nil
		}
		e, err := entryFor(abs, info, exec, watched)
		if err != nil {
			return nil
		}
		snap.Files[slashed] = e
		return nil
	})
	return snap, err
}

// walkGit records .git/hooks and .git/config, the only parts of .git that
// make git run code.
func walkGit(root string, snap *Snapshot, watch *ignore.Matcher) error {
	gitDir := filepath.Join(root, ".git")
	add := func(abs string) {
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		rel, _ := filepath.Rel(root, abs)
		if e, err := entryFor(abs, info, info.Mode().Perm()&0o111 != 0, true); err == nil {
			snap.Files[filepath.ToSlash(rel)] = e
		}
	}
	add(filepath.Join(gitDir, "config"))
	_ = filepath.WalkDir(filepath.Join(gitDir, "hooks"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			add(p)
		}
		return nil
	})
	return filepath.SkipDir
}

func entryFor(abs string, info fs.FileInfo, exec, watched bool) (Entry, error) {
	if info.Size() > maxFileSize {
		return Entry{Hash: fmt.Sprintf("size:%d", info.Size()), Executable: exec, Watched: watched}, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Entry{}, err
	}
	sum := sha256.Sum256(data)
	e := Entry{Hash: hex.EncodeToString(sum[:]), Executable: exec, Watched: watched}
	switch filepath.Base(abs) {
	case "package.json", "composer.json":
		e.Scripts = scriptsOf(data)
	}
	return e, nil
}

// scriptsOf extracts the "scripts" object of a package.json or composer.json.
// Script values may be strings or (composer) lists; both are flattened.
func scriptsOf(data []byte) map[string]string {
	var doc struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	out := make(map[string]string, len(doc.Scripts))
	for k, v := range doc.Scripts {
		out[k] = string(v)
	}
	return out
}

// Change is one difference between two snapshots.
type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // added | modified | removed | became-executable | script-added | script-changed | script-removed
	Detail string `json:"detail,omitempty"`
}

func (c Change) String() string {
	s := fmt.Sprintf("%-18s %s", c.Kind, c.Path)
	if c.Detail != "" {
		s += "  " + c.Detail
	}
	return s
}

// Diff lists what changed from old to now, sorted by path.
func Diff(old, now *Snapshot) []Change {
	var out []Change
	for p, n := range now.Files {
		o, existed := old.Files[p]
		switch {
		case !existed:
			d := ""
			if n.Executable {
				d = "(executable)"
			}
			out = append(out, Change{Path: p, Kind: "added", Detail: d})
			for name := range n.Scripts {
				out = append(out, Change{Path: p, Kind: "script-added", Detail: name})
			}
		case o.Hash != n.Hash:
			out = append(out, Change{Path: p, Kind: "modified"})
			out = append(out, scriptChanges(p, o.Scripts, n.Scripts)...)
		case n.Executable && !o.Executable:
			out = append(out, Change{Path: p, Kind: "became-executable"})
		}
	}
	for p := range old.Files {
		if _, ok := now.Files[p]; !ok {
			out = append(out, Change{Path: p, Kind: "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func scriptChanges(path string, old, now map[string]string) []Change {
	var out []Change
	for name, v := range now {
		switch ov, ok := old[name]; {
		case !ok:
			out = append(out, Change{Path: path, Kind: "script-added", Detail: name + ": " + short(v)})
		case ov != v:
			out = append(out, Change{Path: path, Kind: "script-changed", Detail: name + ": " + short(v)})
		}
	}
	for name := range old {
		if _, ok := now[name]; !ok {
			out = append(out, Change{Path: path, Kind: "script-removed", Detail: name})
		}
	}
	return out
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 100 {
		return s[:97] + "..."
	}
	return s
}

// BaselinePath is where a project's baseline is stored.
func BaselinePath(stateDir, slug string) string {
	return filepath.Join(stateDir, "audit", slug+".json")
}

// Load reads a snapshot; a missing file returns (nil, nil).
func Load(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Save writes a snapshot atomically.
func Save(path string, s *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
