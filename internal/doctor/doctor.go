// Package doctor holds the pure parts of `airlock doctor`: check results, the
// platform notes, and the scan for secret-looking files.
package doctor

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/alexghirelli/airlock/internal/ignore"
)

// Status of a check.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Check is one line of the report.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the full result.
type Report struct {
	Checks []Check `json:"checks"`
}

func (r *Report) Add(name string, st Status, detail, fix string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: st, Detail: detail, Fix: fix})
}

// Failed reports whether any check failed.
func (r *Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// secretPatterns are names that usually hold secrets.
var secretPatterns = []string{
	".env", ".env.*", "!.env.example", "!.env.sample", "!.env.template", "!.env.dist",
	"*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore",
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "*.ppk",
	".netrc", ".npmrc", ".pypirc", ".git-credentials",
	"credentials.json", "service-account*.json", "*secret*.json", "terraform.tfvars", "*.tfstate",
	"wp-config.php", "settings.local.php", ".htpasswd",
}

// FindSecrets returns project files that look like they hold secrets and are
// not hidden by the .airlockignore matcher (may be nil). It stops after limit
// hits or a bounded walk.
func FindSecrets(root string, masked *ignore.Matcher, limit int) []string {
	pat := ignore.FromLines(secretPatterns...)
	var found []string
	seen := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "vendor", ".venv":
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > 200000 || len(found) >= limit {
			return filepath.SkipAll
		}
		slashed := filepath.ToSlash(rel)
		if pat.Match(slashed, false) && !(masked != nil && masked.Match(slashed, false)) {
			found = append(found, slashed)
		}
		return nil
	})
	return found
}

// PlatformNotes returns notes about the host platform that affect airlock.
func PlatformNotes(goos, projectRoot string, rootless bool) []Check {
	var out []Check
	switch goos {
	case "windows":
		out = append(out, Check{Name: "platform", Status: Warn,
			Detail: "Windows host: the project is mounted at a translated path (C:\\x becomes /c/x), so worktrees created inside the sandbox work from WSL, not from native Windows git",
			Fix:    "keep projects inside a WSL2 distribution (e.g. ~/projects) and run airlock there for speed and git path compatibility"})
	case "darwin":
		if !strings.HasPrefix(projectRoot, "/Users/") {
			out = append(out, Check{Name: "platform", Status: Warn,
				Detail: "macOS host: Docker Desktop, OrbStack and Colima share /Users by default; " + projectRoot + " may not be shared with the VM",
				Fix:    "move the project under /Users, or add its parent folder to the runtime's file-sharing settings"})
		}
	case "linux":
		if rootless {
			out = append(out, Check{Name: "platform", Status: Warn,
				Detail: "rootless engine: your host user is root inside containers, so files created in the project by the sandbox user may not belong to you",
				Fix:    "use Podman with --userns=keep-id (not supported by the container backend yet) or run the rootful engine; see docs/platforms.md"})
		}
	}
	return out
}
