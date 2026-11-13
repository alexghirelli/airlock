package config

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	runnerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	// Docker's own rule for service and container names, without a leading "-".
	dockerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	runnerUserRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(:[A-Za-z0-9._-]+)?$`)
)

// runnerErrors checks the [[runner]] tables: names unique and well formed, one
// of compose+service or container, and a mount point in the container.
func runnerErrors(rs []Runner) []error {
	var errs []error
	seen := map[string]bool{}
	for i, r := range rs {
		where := fmt.Sprintf("runner[%d]", i)
		if !runnerNameRe.MatchString(r.Name) {
			errs = append(errs, fmt.Errorf("%s.name: %q must match %s", where, r.Name, runnerNameRe))
			continue
		}
		where = fmt.Sprintf("runner %q", r.Name)
		if seen[r.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate runner name", where))
		}
		seen[r.Name] = true

		files, ferr := r.ComposeFiles()
		if ferr != nil {
			errs = append(errs, fmt.Errorf("%s.compose: %w", where, ferr))
		}
		for _, f := range files {
			if f == "" || strings.ContainsAny(f, "\x00\n\r") || !filepath.IsAbs(ExpandHome(f)) {
				errs = append(errs, fmt.Errorf("%s.compose: %q must be an absolute path (or start with ~)", where, f))
			}
		}
		hasCompose := len(files) > 0 || r.Service != ""
		switch {
		case r.Container != "" && hasCompose:
			errs = append(errs, fmt.Errorf("%s: set either compose and service, or container, not both", where))
		case r.Container != "":
			if !dockerNameRe.MatchString(r.Container) {
				errs = append(errs, fmt.Errorf("%s.container: %q is not a container name", where, r.Container))
			}
		case len(files) == 0 || r.Service == "":
			errs = append(errs, fmt.Errorf("%s: set compose and service, or container", where))
		default:
			if !dockerNameRe.MatchString(r.Service) {
				errs = append(errs, fmt.Errorf("%s.service: %q is not a service name", where, r.Service))
			}
		}
		if err := checkRunnerMount(r.Mount); err != nil {
			errs = append(errs, fmt.Errorf("%s.mount: %w", where, err))
		}
		if r.User != "" && !runnerUserRe.MatchString(r.User) {
			errs = append(errs, fmt.Errorf("%s.user: %q must be USER or USER:GROUP", where, r.User))
		}
	}
	return errs
}

// checkRunnerMount wants an absolute path inside the container, without "..",
// and not the container's root.
func checkRunnerMount(m string) error {
	switch {
	case m == "":
		return fmt.Errorf("is required: where the project root is mounted in the container, such as /var/www/html")
	case strings.ContainsAny(m, "\x00\n\r"):
		return fmt.Errorf("%q contains control characters", m)
	case !strings.HasPrefix(m, "/"):
		return fmt.Errorf("%q must be an absolute path inside the container", m)
	case path.Clean(m) == "/":
		return fmt.Errorf("the container's root is not a mount point")
	}
	for _, part := range strings.Split(m, "/") {
		if part == ".." {
			return fmt.Errorf("%q must not contain ..", m)
		}
	}
	return nil
}
