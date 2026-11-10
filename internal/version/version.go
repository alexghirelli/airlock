// Package version holds the airlock release version.
package version

import (
	"regexp"
	"runtime/debug"
	"strings"
)

const devVersion = "0.1.0-dev"

// Version is set at release time with
// -ldflags "-X github.com/alexghirelli/airlock/internal/version.Version=...".
// Builds made with `go install ...@vX.Y.Z` carry no ldflags, so they read the
// module version from the build information instead.
var Version = devVersion

func init() {
	if Version != devVersion {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v, ok := fromModuleVersion(bi.Main.Version); ok {
			Version = v
		}
	}
}

// fromModuleVersion turns a Go module version ("v0.1.0") into a release version
// ("0.1.0"). Builds outside a git checkout report "(devel)" or nothing and keep
// the dev version; builds inside one get Go's VCS-derived version, such as
// "0.1.1-0.20261110...+dirty", which says which commit they came from.
func fromModuleVersion(v string) (string, bool) {
	if v == "" || v == "(devel)" || !strings.HasPrefix(v, "v") {
		return "", false
	}
	return strings.TrimPrefix(v, "v"), true
}

var badTagChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// Tag is Version made safe for use in a container image tag, which allows only
// letters, digits, underscore, dot and dash (a build from a dirty checkout has a
// "+dirty" suffix, for example).
func Tag() string { return badTagChars.ReplaceAllString(Version, "-") }
