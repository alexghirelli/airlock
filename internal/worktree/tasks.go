package worktree

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Task files are the briefs airlock gives the agent of a worktree: its focus
// (`wt focus`, `wt add <focus>`), its review (`airlock review`) and its handoff
// briefing (`wt handoff`). They live in a folder the host owns, outside the
// worktree and its git directory, which the sandbox mounts read-only: an agent
// can neither rewrite what the host reads and writes, nor what the next agent
// is told at its start.
//
// Everything here is host-side. The folder is 0755 and the files 0644 (see prepareDir), and no
// path that the sandbox can influence is ever used: the worktree's name comes
// from the host, and a link, a FIFO or any other non-regular file in the folder
// is never followed or opened.

const (
	focusFile   = "focus.md"   // written by `wt focus`, `wt add <focus>` and `review <change> <focus>`
	reviewFile  = "review.md"  // written by `airlock review`
	handoffFile = "handoff.md" // written by `wt handoff`, read by an agent that starts fresh
)

// MaxTaskBytes caps a task file. A write above it is refused, and a file above
// it is ignored when read: nothing that large is a brief.
const MaxTaskBytes = 64 << 10

// ValidName reports whether name can name a worktree, and so its task folder:
// the rule PathFor applies, which keeps a name out of every path but its own.
func ValidName(name string) bool {
	return nameOK.MatchString(name) && !strings.Contains(name, "..")
}

// TaskDir is the folder of one worktree's task files under root, the folder of
// a sandbox's task files. The name must be a valid worktree name.
func TaskDir(root, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid worktree name %q", name)
	}
	if root == "" {
		return "", errors.New("no folder for the task files")
	}
	return filepath.Join(root, name), nil
}

// prepareDir makes dir a real folder, created 0755 when it is new. A link or a
// file in its place is refused: the folder is the host's, and nothing in it is
// to be followed. It is readable by every user on purpose: the sandbox reads it
// through a read-only mount as the container's agent, whose user id is the host
// user's on Linux but is mapped by the file sharing layer on Docker Desktop, and
// a task that silently went missing there would be hard to find. The text is
// not secret (it is shown to the agent); only the host can change it.
func prepareDir(dir string) error {
	if dir == "" {
		return errors.New("no folder for the task files")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	return nil
}

// readTask returns the text of a task file, or "" when there is none or it
// cannot be used: a link, a FIFO or any other non-regular file, or one over
// MaxTaskBytes. The file is checked before it is opened, so a FIFO never blocks
// the read, and the open goes through os.Root, so no link can lead out of dir.
func readTask(dir, file string) string {
	if dir == "" {
		return ""
	}
	fi, err := os.Lstat(filepath.Join(dir, file))
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxTaskBytes {
		return ""
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ""
	}
	defer root.Close()
	f, err := root.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxTaskBytes+1))
	if err != nil || len(b) > MaxTaskBytes {
		return ""
	}
	return string(b)
}

// writeTask replaces a task file. The text goes to a temporary file in the same
// folder (0644) and is renamed over the old one, so a reader sees either the old
// file or the new one, never half of one. A non-regular file in the way is refused.
func writeTask(dir, file string, text []byte) error {
	if len(text) > MaxTaskBytes {
		return fmt.Errorf("the task is larger than %d KiB", MaxTaskBytes>>10)
	}
	if err := prepareDir(dir); err != nil {
		return err
	}
	target := filepath.Join(dir, file)
	if fi, err := os.Lstat(target); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file; remove it first", target)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+file+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	_, werr := tmp.Write(text)
	if cerr := tmp.Chmod(0o644); werr == nil {
		werr = cerr
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Rename(tmp.Name(), target)
}

// removeTask deletes a task file, if there is one. A link is removed as a link.
func removeTask(dir, file string) error {
	if dir == "" {
		return errors.New("no folder for the task files")
	}
	if err := os.Remove(filepath.Join(dir, file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SetFocus sets what the agent of the worktree whose task folder is dir
// concentrates on. Empty text clears it.
func SetFocus(dir, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return removeTask(dir, focusFile)
	}
	return writeTask(dir, focusFile, []byte("# Focus of this worktree\n\n"+text+"\n"))
}

// Focus returns the focus set with SetFocus, without its heading; "" when there is none.
func Focus(dir string) string {
	return strings.TrimSpace(strings.TrimPrefix(readTask(dir, focusFile), "# Focus of this worktree"))
}

// Brief returns the task that `airlock review` wrote for a worktree, or "".
func Brief(dir string) string { return readTask(dir, reviewFile) }

// WriteHandoff replaces the handoff briefing of a worktree.
func WriteHandoff(dir, text string) error { return writeTask(dir, handoffFile, []byte(text)) }

// ClearHandoff deletes the handoff briefing of a worktree, if it has one.
func ClearHandoff(dir string) error { return removeTask(dir, handoffFile) }

// RemoveTasks deletes the task folder of a worktree with everything in it. A
// link in its place is removed as a link, never followed.
func RemoveTasks(dir string) error {
	if dir == "" {
		return errors.New("no folder for the task files")
	}
	return os.RemoveAll(dir)
}
