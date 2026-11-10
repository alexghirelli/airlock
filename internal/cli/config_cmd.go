package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"airlock/internal/config"
)

func (e *env) userConfigPath() string {
	if e.a.cfgFile != "" {
		return e.a.cfgFile
	}
	return config.UserConfigPath()
}

func (e *env) cmdConfig() (int, error) {
	sub := "show"
	if len(e.a.rest) > 0 {
		sub = e.a.rest[0]
	}
	path := e.userConfigPath()
	switch sub {
	case "path":
		fmt.Fprintln(e.stdout, path)
	case "init":
		if _, err := os.Stat(path); err == nil && !e.a.force {
			return 1, fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
		if err := writeTemplate(path); err != nil {
			return 1, err
		}
		fmt.Fprintf(e.stderr, "airlock: wrote %s\n", path)
	case "show":
		var p string
		if sp, err := e.resolveForShow(); err == nil {
			p = sp
		}
		res, err := config.Load(config.LoadOptions{ProjectDir: p, UserConfigPath: path, Flags: e.flags()})
		if err != nil {
			return 2, err
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(e.stderr, "airlock: warning: %s\n", w)
		}
		out, err := toml.Marshal(res.Config)
		if err != nil {
			return 1, err
		}
		_, err = e.stdout.Write(out)
		return 0, err
	case "edit":
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := writeTemplate(path); err != nil {
				return 1, err
			}
		}
		editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
		cmd := exec.Command(editor, path)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.stdout, e.stderr
		if err := cmd.Run(); err != nil {
			return 1, fmt.Errorf("%s: %w", editor, err)
		}
	default:
		return 2, fmt.Errorf("unknown config command %q (init, show, path, edit)", sub)
	}
	return 0, nil
}

// resolveForShow returns the project root if the current directory has one.
func (e *env) resolveForShow() (string, error) {
	l, err := e.load()
	if err != nil {
		return "", err
	}
	return l.project.Root, nil
}

func writeTemplate(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(config.Template), 0o644)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// inProject reports whether the working directory is inside a git repository.
func inProject() bool {
	dir, err := os.Getwd()
	if err != nil {
		return false
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

func mustUser(e *env) *config.Result {
	res, err := e.loadUser()
	if err != nil {
		fmt.Fprintf(e.stderr, "airlock: %v\n", err)
		os.Exit(2)
	}
	return res
}
