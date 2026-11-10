// Package termio wraps the user's terminal for interactive attach: raw mode,
// size, and resize notifications.
package termio

import (
	"errors"
	"io"
	"os"

	"golang.org/x/term"
)

// Terminal is the controlling terminal of the airlock process.
type Terminal struct {
	In  io.Reader
	Out io.Writer
	fd  int
}

// Open returns the terminal, or an error when stdin or stdout is not one.
func Open() (*Terminal, error) {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return nil, errors.New("this command needs an interactive terminal (no TTY on stdin/stdout)")
	}
	return &Terminal{In: os.Stdin, Out: os.Stdout, fd: in}, nil
}

// IsTerminal reports whether stdin and stdout are both terminals.
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Size returns the terminal width and height, falling back to 80x24, which is
// what minimal SSH clients may report.
func (t *Terminal) Size() (w, h int) {
	w, h, err := term.GetSize(t.fd)
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// MakeRaw switches the terminal to raw mode and returns the restore function.
func (t *Terminal) MakeRaw() (restore func(), err error) {
	state, err := term.MakeRaw(t.fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(t.fd, state) }, nil
}
