// Package hostgit is the one way airlock runs git on the host.
//
// The host runs git on repositories the sandbox agent can write to. Git finds
// a repository by itself, from the working directory, a worktree's ".git" file
// or a "commondir" file, and then reads config, attributes and hooks from
// wherever it landed. An agent that rewrites those files can make the host run
// its own commands (core.fsmonitor, filter drivers, core.sshCommand). So this
// package never lets git look for a repository: every call pins the git
// directory, the common directory and the working tree, to paths derived from
// the repository root that the host verified.
//
// Trust. Only the user's own checkout is trusted, and what is trusted is the
// path, never the files at it. A main checkout is "<root>/.git", a real
// directory; a linked worktree is "<root>/.git/worktrees/<name>", found from the
// host side by the admin directory whose "gitdir" names the worktree. The
// worktree's own ".git" file is never followed: the agent writes it. A
// checkout that lies inside a worktrees folder (".worktrees", ".isolated") is
// refused as a root, since the agent writes there too. A root that is a
// ".git" file of its own (a submodule) is refused: its git directory would be
// named by a file the agent may have written.
//
// Config. Git reads "config" from the common directory, which the sandbox
// mounts read-only (with "hooks"). It reads "config.worktree" from the git
// directory only when extensions.worktreeConfig is set, and that file is not
// protected in the main checkout, nor can the host tell a real worktree admin
// directory from one the agent made. So when the common config sets
// extensions.worktreeConfig, git runs on a private snapshot of HEAD, index and
// sparse-checkout (no config.worktree), and mutating calls copy HEAD and the
// index back. Otherwise git runs on the admin directory itself. The "-c"
// pins below are a second layer: they override the keys that run code even in
// files the agent cannot write.
package hostgit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout bounds one git call unless Options.Timeout says otherwise.
const DefaultTimeout = 60 * time.Second

// DefaultMaxOutput bounds what Output keeps of a call's standard output.
const DefaultMaxOutput = 16 << 20

// ErrTooLarge is returned when a call writes more than its output limit.
var ErrTooLarge = errors.New("too much output")

const (
	maxSmallFile = 64 << 10 // gitdir, commondir and the like
	maxStderr    = 64 << 10 // kept for error messages
)

// pins are the options every call carries. They override config keys that run
// programs or change what git reads, whatever file they come from.
var pins = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.sshCommand=ssh",
	"-c", "protocol.ext.allow=never",
	"-c", "protocol.file.allow=user",
	"-c", "core.pager=cat",
	"-c", "diff.external=",
	"-c", "core.attributesFile=/dev/null",
	"-c", "diff.relative=false",
	// A nested repository (a gitlink in the tree) has its own configuration inside the
	// working tree, which the agent writes. Git would start a child process there to
	// ask whether the submodule is dirty, and a clean or smudge filter defined in that
	// configuration would run on the host. Submodules are never looked into.
	"-c", "diff.ignoreSubmodules=all",
	"-c", "status.submoduleSummary=false",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "push.recurseSubmodules=no",
}

// strippedVars are the environment variables that point git at another
// repository, another object store or other configuration, or that name a
// program git runs. The caller's values never reach git.
var strippedVars = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_CONFIG": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_COUNT": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_QUARANTINE_PATH": true, "GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_ASKPASS": true,
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "GIT_SSH_VARIANT": true, "GIT_PROXY_COMMAND": true,
	"GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true, "GIT_EXEC_PATH": true, "GIT_TEMPLATE_DIR": true,
	"GIT_REPLACE_REF_BASE": true, "GIT_ATTR_SOURCE": true, "GIT_PREFIX": true,
}

// Options tunes one call. The zero value is a read-only query with the defaults.
type Options struct {
	// Timeout bounds the call; 0 means DefaultTimeout.
	Timeout time.Duration
	// MaxOutput bounds Output's standard output; 0 means DefaultMaxOutput.
	MaxOutput int64
	// Writes says the call may change the index or HEAD of the working tree.
	// On a snapshot (see the package doc) they are copied back after success.
	Writes bool
}

// Location is a repository checkout the host may run git on: the main
// checkout, or one linked worktree of it.
type Location struct {
	root, tree string
	// admin is the linked worktree's admin directory, "" for the main checkout.
	admin string
	// worktreeConfig is extensions.worktreeConfig in the common config.
	worktreeConfig bool
}

// Root is the main checkout of the repository.
func (l *Location) Root() string { return l.root }

// Tree is the working tree the calls run in: the main checkout or the worktree.
func (l *Location) Tree() string { return l.tree }

// Linked reports whether the location is a linked worktree.
func (l *Location) Linked() bool { return l.admin != "" }

// Main returns the location of the main checkout at root, after checking that
// its git directory is a real directory and nothing redirects it.
func Main(root string) (*Location, error) {
	real, err := realPath(root)
	if err != nil {
		return nil, err
	}
	if inWorktreesFolder(real) {
		return nil, fmt.Errorf("%s is inside a worktrees folder, and git runs only on the project's own checkout; run airlock from the project", real)
	}
	gd := filepath.Join(real, ".git")
	fi, err := os.Lstat(gd)
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", real, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: .git is not a directory (a submodule, or a worktree of another checkout); run airlock from the repository's main checkout", real)
	}
	if _, err := os.Lstat(filepath.Join(gd, "commondir")); err == nil {
		return nil, fmt.Errorf("%s/.git has a commondir file, which points git at another directory; refusing to run git there", real)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return &Location{root: real, tree: real, worktreeConfig: worktreeConfigOn(gd)}, nil
}

// Open returns the location of the checkout at tree, a working tree of the
// repository whose main checkout is root. tree may be root itself.
func Open(root, tree string) (*Location, error) {
	main, err := Main(root)
	if err != nil {
		return nil, err
	}
	if tree == "" {
		return main, nil
	}
	real, err := realPath(tree)
	if err != nil {
		return nil, err
	}
	if real == main.root {
		return main, nil
	}
	return main.Worktree(real)
}

// Worktree returns the location of the linked worktree at path. The worktree's
// own ".git" file is not read: the admin directory is the one, inside this
// repository's ".git/worktrees", whose "gitdir" file names the worktree. Zero
// matches, or more than one, is an error; so is an admin directory whose
// "commondir" is not "../..".
func (l *Location) Worktree(path string) (*Location, error) {
	if l.Linked() {
		return nil, errors.New("a worktree is looked up from the main checkout")
	}
	tree, err := realPath(path)
	if err != nil {
		return nil, err
	}
	admin, err := findAdmin(l.root, tree)
	if err != nil {
		return nil, err
	}
	return &Location{root: l.root, tree: tree, admin: admin, worktreeConfig: l.worktreeConfig}, nil
}

// MainOfWorktree returns the main checkout of the linked worktree at tree, or ""
// when that cannot be verified. Its ".git" file is never read for a worktree
// in a worktrees folder: the agent writes that file, and the folder tells where
// the main checkout is, one level above it. The main checkout must then name
// the worktree in its own ".git/worktrees". Any other worktree (the user's own,
// beside the project, which the agent cannot write) is taken as its ".git" file
// says, once the admin directory it names points back at it.
func MainOfWorktree(tree string) string {
	real, err := realPath(tree)
	if err != nil {
		return ""
	}
	if folder := worktreesFolderOf(real); folder != "" {
		main := filepath.Dir(folder)
		if verifyMain(main, real) != nil {
			return ""
		}
		return main
	}
	if inWorktreesFolder(real) {
		return ""
	}
	raw, err := readSmall(filepath.Join(real, ".git"))
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(raw), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(real, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	marker := string(filepath.Separator) + ".git" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
	i := strings.LastIndex(gitdir, marker)
	if i < 0 {
		return ""
	}
	main := gitdir[:i]
	if main == real || strings.HasPrefix(main, real+string(filepath.Separator)) {
		return "" // a repository cannot sit inside the worktree it has
	}
	main, err = realPath(main)
	if err != nil || verifyMain(main, real) != nil {
		return ""
	}
	return main
}

// worktreesFolderOf returns the worktrees folder a worktree sits in directly
// (".worktrees", or ".isolated" inside it), or "".
func worktreesFolderOf(tree string) string {
	parent := filepath.Dir(tree)
	switch filepath.Base(parent) {
	case ".worktrees":
		return parent
	case ".isolated":
		if grand := filepath.Dir(parent); filepath.Base(grand) == ".worktrees" {
			return grand
		}
	}
	return ""
}

// verifyMain checks that main is a repository whose git directory is a real
// directory and names the worktree at tree.
func verifyMain(main, tree string) error {
	if fi, err := os.Lstat(filepath.Join(main, ".git")); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s has no git directory", main)
	}
	_, err := findAdmin(main, tree)
	return err
}

// inWorktreesFolder reports whether a path lies inside a folder where airlock
// puts worktrees, whose contents the agent writes.
func inWorktreesFolder(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".worktrees" || part == ".isolated" {
			return true
		}
	}
	return false
}

func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return real, nil
}

// findAdmin finds the admin directory of the worktree at tree, under root's
// ".git/worktrees". See Worktree.
func findAdmin(root, tree string) (string, error) {
	wts := filepath.Join(root, ".git", "worktrees")
	fi, err := os.Lstat(wts)
	if err != nil {
		return "", fmt.Errorf("%s has no linked worktrees: %w", root, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory; refusing to read worktrees there", wts)
	}
	want := filepath.Join(tree, ".git")
	ents, err := os.ReadDir(wts)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range ents {
		admin := filepath.Join(wts, e.Name())
		if fi, err := os.Lstat(admin); err != nil || !fi.IsDir() {
			continue
		}
		raw, err := readSmall(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue
		}
		target := strings.TrimSpace(raw)
		if !filepath.IsAbs(target) {
			target = filepath.Join(admin, target)
		}
		if filepath.Clean(target) == want {
			found = append(found, admin)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%s is not a linked worktree of %s", tree, root)
	case 1:
	default:
		return "", fmt.Errorf("%s is named by %d git directories under %s; refusing to choose", tree, len(found), wts)
	}
	common, err := readSmall(filepath.Join(found[0], "commondir"))
	if err != nil || strings.TrimSpace(common) != "../.." {
		return "", fmt.Errorf("the git directory %s does not belong to %s (its commondir is not ../..); refusing to run git there", found[0], root)
	}
	return found[0], nil
}

// readSmall reads a small regular file. A symlink or anything else is an error.
func readSmall(p string) (string, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", p)
	}
	if fi.Size() > maxSmallFile {
		return "", fmt.Errorf("%s is too large", p)
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// worktreeConfigOn says whether extensions.worktreeConfig is set in the common
// config. It fails closed: when that cannot be read, the answer is yes, and the
// snapshot path is taken.
func worktreeConfigOn(gitDir string) bool {
	cfg := filepath.Join(gitDir, "config")
	fi, err := os.Lstat(cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil || !fi.Mode().IsRegular() {
		return true
	}
	cmd := exec.Command("git", "config", "--file", cfg, "--type=bool", "--get", "extensions.worktreeConfig")
	cmd.Dir = filepath.Dir(gitDir)
	cmd.Env = environ("", "", "")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false // the key is not set
		}
		return true
	}
	return strings.TrimSpace(string(out)) == "true"
}

// gitDirPath is the directory git's per-worktree state lives in: the admin
// directory, or the main checkout's ".git".
func (l *Location) gitDirPath() string {
	if l.admin != "" {
		return l.admin
	}
	return filepath.Join(l.root, ".git")
}

// Output runs git and returns its standard output, unchanged. A call that
// writes more than the limit fails with ErrTooLarge.
func (l *Location) Output(ctx context.Context, o Options, args ...string) (string, error) {
	out := &capped{max: o.maxOutput()}
	if err := l.run(ctx, o, out, args); err != nil {
		return "", err
	}
	return out.buf.String(), nil
}

// Stream runs git with its standard output and error going to the given writers.
func (l *Location) Stream(ctx context.Context, o Options, stdout, stderr io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	cmd, done, err := l.command(ctx, o, args)
	if err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	done(err == nil)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("git %s: timed out after %s", args[0], o.timeout())
	}
	return err
}

func (l *Location) run(ctx context.Context, o Options, stdout io.Writer, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	cmd, done, err := l.command(ctx, o, args)
	if err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	errb := &capped{max: maxStderr, lenient: true}
	cmd.Stdout, cmd.Stderr = stdout, errb
	err = cmd.Run()
	done(err == nil)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("git %s: timed out after %s", args[0], o.timeout())
	}
	if c, ok := stdout.(*capped); ok && c.over {
		return fmt.Errorf("git %s: %w (more than %d bytes)", args[0], ErrTooLarge, c.max)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", args[0], msg)
	}
	return nil
}

// command builds the pinned git command. The returned function ends the call:
// it copies HEAD and the index back when the call wrote them, and removes the
// snapshot.
func (l *Location) command(ctx context.Context, o Options, args []string) (*exec.Cmd, func(ok bool), error) {
	if len(args) == 0 {
		return nil, nil, errors.New("no git command")
	}
	src := l.gitDirPath()
	gitDir, snap := src, ""
	if l.worktreeConfig {
		d, err := snapshot(src, filepath.Join(l.root, ".git"))
		if err != nil {
			return nil, nil, err
		}
		gitDir, snap = d, d
	}
	full := append(append([]string{}, pins...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = l.tree
	cmd.Env = environ(gitDir, filepath.Join(l.root, ".git"), l.tree)
	cmd.WaitDelay = 5 * time.Second
	done := func(ok bool) {
		if snap == "" {
			return
		}
		if ok && o.Writes {
			copyBack(snap, src)
		}
		_ = os.RemoveAll(snap)
	}
	return cmd, done, nil
}

// environ is the environment of a pinned call: the caller's, minus the variables
// strippedVars names, plus the pins. AIRLOCK_SSH, when set, is the ssh program
// git uses (the tests use a fake one).
func environ(gitDir, common, tree string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strippedVars[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	if gitDir != "" {
		env = append(env, "GIT_DIR="+gitDir, "GIT_COMMON_DIR="+common, "GIT_WORK_TREE="+tree)
	}
	if ssh := os.Getenv("AIRLOCK_SSH"); ssh != "" {
		env = append(env, "GIT_SSH_COMMAND="+shellQuote(ssh))
	}
	return env
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// snapshotFiles are what git reads from a worktree's git directory and that
// the host needs: where HEAD points, the index, and the sparse-checkout patterns.
var snapshotFiles = []string{"HEAD", "index", "info/sparse-checkout"}

// snapshot copies the per-worktree files of src into a private directory that
// only this process knows about, and returns it. config.worktree is not copied.
// The directory gets a commondir file, as a worktree's admin directory has: git
// resolves refs through it, and GIT_COMMON_DIR alone does not do that.
func snapshot(src, common string) (string, error) {
	dir, err := os.MkdirTemp("", "airlock-git-")
	if err != nil {
		return "", err
	}
	for _, name := range snapshotFiles {
		if err := copyRegular(filepath.Join(src, name), filepath.Join(dir, name), 0o600); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "commondir"), []byte(common+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// copyBack writes HEAD and the index from a snapshot back to the git directory
// they came from, through a temporary file and a rename. Errors are dropped:
// the next call reads what is there.
func copyBack(snap, src string) {
	for _, name := range []string{"HEAD", "index"} {
		dst := filepath.Join(src, name)
		tmp := dst + ".airlock-tmp"
		if err := copyRegular(filepath.Join(snap, name), tmp, 0o644); err != nil {
			_ = os.Remove(tmp)
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
		}
	}
}

// copyRegular copies a regular file of any size. A symlink or other entry at
// from is an error, so a link in the agent's tree is never followed.
func copyRegular(from, to string, perm os.FileMode) error {
	fi, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", from)
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// capped keeps up to max bytes. Past the limit a strict one fails the write,
// which stops git; a lenient one keeps the first bytes and drops the rest.
type capped struct {
	buf     bytes.Buffer
	max     int64
	over    bool
	lenient bool
}

func (c *capped) Write(p []byte) (int, error) {
	if int64(c.buf.Len()+len(p)) > c.max {
		c.over = true
		if c.lenient {
			room := int(c.max) - c.buf.Len()
			if room > 0 {
				c.buf.Write(p[:room])
			}
			return len(p), nil
		}
		return 0, ErrTooLarge
	}
	return c.buf.Write(p)
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

func (o Options) maxOutput() int64 {
	if o.MaxOutput <= 0 {
		return DefaultMaxOutput
	}
	return o.MaxOutput
}
