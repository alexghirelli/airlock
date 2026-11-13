// Package imagebuild assembles build contexts and decides image tags.
package imagebuild

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alexghirelli/airlock"
	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/version"
)

// Registry is where the release workflow publishes the base image.
// AIRLOCK_IMAGE_REGISTRY points airlock at a mirror or a fork instead.
const Registry = "ghcr.io/alexghirelli/airlock"

func registry() string {
	if r := os.Getenv("AIRLOCK_IMAGE_REGISTRY"); r != "" {
		return strings.TrimSuffix(r, "/")
	}
	return Registry
}

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$`)

// PublishedRef returns the published image that matches this airlock release,
// or false for a development build (nothing is published for those) or when the
// user asked for local builds with AIRLOCK_BUILD_LOCAL=1.
func PublishedRef() (string, bool) {
	if os.Getenv("AIRLOCK_BUILD_LOCAL") == "1" || !releaseVersion.MatchString(version.Version) {
		return "", false
	}
	return registry() + ":" + version.Tag(), true
}

// LayerDockerfile is what the configuration adds to the downloaded image: extra
// apt packages. It returns nil when nothing is added. Agents are not here: Claude
// Code and other Pi versions are installed into the tools volume when needed.
func LayerDockerfile(from string, c *config.Config) []byte {
	if len(c.Extras.Apt) == 0 {
		return nil
	}
	return ExtrasDockerfile(from, c.Extras.Apt)
}

// BaseRef returns the tag of the base image for the given configuration. It
// changes whenever a build input (extra packages, the pinned Pi) changes, so
// editing the config rebuilds the image instead of silently reusing a stale one.
func BaseRef(c *config.Config) string {
	return fmt.Sprintf("airlock:%s-%s", version.Tag(), shortHash(baseInputs(c)))
}

func baseInputs(c *config.Config) string {
	apt := append([]string(nil), c.Extras.Apt...)
	sort.Strings(apt)
	return strings.Join([]string{
		"pi=" + config.PinnedPiVersion,
		"apt=" + strings.Join(apt, ","),
		"src=" + assetsHash(),
	}, ";")
}

// assetsHash fingerprints the embedded image sources, so a new airlock build
// with a changed Dockerfile, entrypoint or team script never reuses an old image.
func assetsHash() string {
	h := sha256.New()
	_ = fs.WalkDir(airlock.Assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "image/AGENTS.md.tmpl" {
			return err
		}
		data, _ := airlock.Assets.ReadFile(p)
		fmt.Fprintf(h, "%s:%d:", p, len(data))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// BaseArgs are the Dockerfile build arguments for the base image. The image has
// the pinned Pi only; other Pi versions and the other agents are installed later.
func BaseArgs(c *config.Config) map[string]string {
	return map[string]string{
		"NODE_VERSION": config.PinnedNodeMajor,
		"PI_VERSION":   config.PinnedPiVersion,
		"EXTRA_APT":    strings.Join(c.Extras.Apt, " "),
	}
}

// BaseContext returns the tar build context of the base image.
func BaseContext() (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for _, f := range []struct{ src, dst string }{
		{"image/Dockerfile", "Dockerfile"},
		{"image/entrypoint.sh", "entrypoint.sh"},
		{"image/init.sh", "init.sh"},
		{"image/session.sh", "session.sh"},
		{"image/herdr-up.sh", "herdr-up.sh"},
		{"image/profile-path.sh", "profile-path.sh"},
		{"image/pi-wrapper.sh", "pi-wrapper.sh"},
		{"image/npm-wrapper.sh", "npm-wrapper.sh"},
		{"image/claude-wrapper.sh", "claude-wrapper.sh"},
		{"image/agent-install.sh", "agent-install.sh"},
		{"image/agent-session.sh", "agent-session.sh"},
		{"image/idle-watch.sh", "idle-watch.sh"},
	} {
		data, err := airlock.Assets.ReadFile(f.src)
		if err != nil {
			return nil, err
		}
		if err := add(f.dst, data, 0o755); err != nil {
			return nil, err
		}
	}
	err := fs.WalkDir(airlock.Assets, "team", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := airlock.Assets.ReadFile(p)
		if err != nil {
			return err
		}
		return add(p, data, 0o755)
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// ProjectRef returns the tag of the per-project image, or "" when the project
// needs none (no Dockerfile and no project extras).
func ProjectRef(slug, base string, apt []string, dockerfile string) string {
	if len(apt) == 0 && dockerfile == "" {
		return ""
	}
	sorted := append([]string(nil), apt...)
	sort.Strings(sorted)
	return fmt.Sprintf("airlock-project:%s-%s", slug, shortHash(base+";"+strings.Join(sorted, ",")+";"+dockerfile))
}

// ExtrasDockerfile is the generated layer that installs project-requested
// Debian packages on top of an image. The package names were validated when
// the config was loaded.
func ExtrasDockerfile(from string, apt []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\nUSER root\n", from)
	if len(apt) > 0 {
		fmt.Fprintf(&b, "RUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.Join(apt, " "))
	}
	b.WriteString("USER root\n")
	return []byte(b.String())
}

// SingleFileContext wraps one Dockerfile as a build context.
func SingleFileContext(dockerfile []byte) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(dockerfile); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
