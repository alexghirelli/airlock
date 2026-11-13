package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/sandbox"
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
	if e.a.project {
		return e.cmdProjectConfig(sub)
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
		e.reportLayers(res)
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
		return openEditor(e, path)
	default:
		return 2, fmt.Errorf("unknown config command %q (init, show, path, edit)", sub)
	}
	return 0, nil
}

// cmdProjectConfig handles config init|path|edit --project: the settings file
// of the project around the current directory, in the projects folder.
func (e *env) cmdProjectConfig(sub string) (int, error) {
	switch sub {
	case "path", "init", "edit":
	default:
		return 2, fmt.Errorf("config %s --project: use init, path or edit", sub)
	}
	p, err := sandbox.Resolve("")
	if err != nil {
		return 2, err
	}
	root := p.RepoRoot() // a worktree's settings are its main checkout's
	dir, ok := config.ProjectsDirFor(e.userConfigPath(), os.Getenv)
	if !ok {
		return 2, fmt.Errorf("config %s --project: the settings files live in the projects folder next to the user config, which needs the user config to be called config.toml; set %s to name the folder", sub, config.ProjectsDirEnv)
	}
	path, exists, err := config.ProjectFileChoice(dir, root)
	if err != nil {
		return 1, err
	}
	switch sub {
	case "path":
		fmt.Fprintln(e.stdout, path)
		return 0, nil
	case "init":
		if exists && !e.a.force {
			return 1, fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
		if err := writeProjectFile(path, root, e.a.force); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return 1, fmt.Errorf("%s was created meanwhile (use --force to overwrite)", path)
			}
			return 1, err
		}
		e.errf("airlock: wrote %s", path)
		return 0, nil
	default: // edit
		if !exists {
			if err := writeProjectFile(path, root, false); err != nil && !errors.Is(err, fs.ErrExist) {
				return 1, err
			}
		}
		return openEditor(e, path)
	}
}

// writeProjectFile writes the skeleton of a project settings file for root, mode
// 0600. Without force the file must not exist (an existing one is fs.ErrExist);
// with force it is replaced through a temporary file and a rename.
func writeProjectFile(path, root string, force bool) error {
	body, err := config.ProjectFileTemplate(root)
	if err != nil {
		return err
	}
	if force {
		return config.ReplaceProjectFile(path, body)
	}
	return config.CreateProjectFile(path, body)
}

// openEditor opens path in $VISUAL, $EDITOR or vi, and waits for it.
func openEditor(e *env, path string) (int, error) {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.stdout, e.stderr
	if err := cmd.Run(); err != nil {
		return 1, fmt.Errorf("%s: %w", editor, err)
	}
	return 0, nil
}

// reportLayers says, on stderr, which configuration layers applied.
func (e *env) reportLayers(res *config.Result) {
	user := res.UserConfigPath + " (not created: defaults only)"
	if res.UserConfigExists {
		user = res.UserConfigPath
	}
	e.errf("airlock: layers: built-in defaults, user config %s", user)
	for _, f := range res.ProjectFiles {
		e.errf("airlock: layers: project settings %s", f)
	}
	if res.ProjectConfigPath != "" {
		e.errf("airlock: layers: %s (untrusted: only the keys a project may set)", res.ProjectConfigPath)
	}
	e.errf("airlock: layers: then flags and AIRLOCK_* environment variables")
}

// resolveForShow returns the project root if the current directory has one. It is
// the repository's main checkout for a worktree, as load uses it: the settings
// files and .airlock.toml of a project are found from there.
func (e *env) resolveForShow() (string, error) {
	l, err := e.load()
	if err != nil {
		return "", err
	}
	return l.project.RepoRoot(), nil
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
