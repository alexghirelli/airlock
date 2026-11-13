package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectsDirEnv names the projects folder explicitly.
const ProjectsDirEnv = "AIRLOCK_PROJECTS_DIR"

// ProjectsDirFor is the folder of project settings files for the user config at
// userConfigPath. AIRLOCK_PROJECTS_DIR names it when set. Otherwise it is
// <dir of the config>/projects, but only when the config file is called
// config.toml: for any other name the folder would be a guess, so there is none
// and ok is false.
func ProjectsDirFor(userConfigPath string, getenv func(string) string) (dir string, ok bool) {
	if v := getenv(ProjectsDirEnv); v != "" {
		return v, true
	}
	if filepath.Base(userConfigPath) == "config.toml" {
		return filepath.Join(filepath.Dir(userConfigPath), ProjectFilesDirName), true
	}
	return "", false
}

// ConfigFolders are the folders the sandbox must never reach, because the trusted
// settings live there: the airlock configuration folder, the folder of the user
// config file in use, and the projects folder (when there is one).
func ConfigFolders(userConfigPath, projectsDir string) []string {
	folders := []string{UserConfigDir()}
	if userConfigPath != "" {
		folders = append(folders, filepath.Dir(userConfigPath))
	}
	if projectsDir != "" {
		folders = append(folders, projectsDir)
	}
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		if f != "" {
			out = append(out, filepath.Clean(f))
		}
	}
	return out
}

// defaultConfigFolders are the protected folders as the environment alone says
// them: the user config and projects folders that AIRLOCK_CONFIG and
// AIRLOCK_PROJECTS_DIR, or the defaults, give. Load adds the ones of --config.
func defaultConfigFolders() []string {
	cfg := UserConfigPath()
	projects, _ := ProjectsDirFor(cfg, os.Getenv)
	return ConfigFolders(cfg, projects)
}

// CheckFolderReach refuses a path (a mount, a project root) that is one of the
// folders, holds one, or lies inside one. what names the path in the error.
func CheckFolderReach(path, what string, folders []string) error {
	for _, f := range folders {
		if reaches(path, f) {
			return fmt.Errorf("%s %s reaches %s, where your trusted airlock settings live: the sandbox would be able to rewrite them, so this is refused", what, path, f)
		}
	}
	return nil
}

// reaches reports whether path is folder, holds it or lies inside it. Both are
// resolved through symlinks first, and compared case-insensitively on macOS and
// Windows, where the file system does not tell names apart.
func reaches(path, folder string) bool {
	return reachesFold(realPath(path), realPath(folder), foldsCase())
}

func foldsCase() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

// reachesFold is reaches for two resolved paths, with the case rule given.
func reachesFold(a, b string, fold bool) bool {
	if fold {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return a == b || insideOrEqual(a, b) || insideOrEqual(b, a)
}

// realPath resolves p through symlinks. A part that does not exist yet is kept
// as written, after the longest existing prefix has been resolved.
func realPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	cur, rest := abs, ""
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
