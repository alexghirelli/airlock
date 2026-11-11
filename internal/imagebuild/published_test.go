package imagebuild

import (
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/version"
)

func TestPublishedRefOnlyForReleases(t *testing.T) {
	old := version.Version
	defer func() { version.Version = old }()
	t.Setenv("AIRLOCK_BUILD_LOCAL", "")
	for v, want := range map[string]string{
		"0.2.0":      Registry + ":0.2.0",
		"1.0.0-rc.1": Registry + ":1.0.0-rc.1",
		"0.1.0-dev":  Registry + ":0.1.0-dev",
		// What a build from a git checkout reports: nothing is published for it.
		"0.1.1-0.20261110165959-abcdef123456+dirty": "",
		"": "",
	} {
		version.Version = v
		got, ok := PublishedRef()
		if (want != "") != ok || got != want {
			t.Errorf("version %q: got %q, %v; want %q", v, got, ok, want)
		}
	}
}

func TestPublishedRefCanBeTurnedOff(t *testing.T) {
	old := version.Version
	defer func() { version.Version = old }()
	version.Version = "0.2.0"
	t.Setenv("AIRLOCK_BUILD_LOCAL", "1")
	if ref, ok := PublishedRef(); ok {
		t.Errorf("AIRLOCK_BUILD_LOCAL=1 still gives %q", ref)
	}
}

func TestLayerDockerfile(t *testing.T) {
	plain := &config.Config{}
	plain.Extras.ClaudeCode = "false"
	if df := LayerDockerfile("img", plain); df != nil {
		t.Errorf("nothing to add, got a layer:\n%s", df)
	}
	withApt := *plain
	withApt.Extras.Apt = []string{"ffmpeg", "tree"}
	df := string(LayerDockerfile("img", &withApt))
	if !strings.HasPrefix(df, "FROM img\n") || !strings.Contains(df, "apt-get install -y --no-install-recommends ffmpeg tree") || strings.Contains(df, "claude") {
		t.Errorf("apt layer:\n%s", df)
	}
	withClaude := *plain
	withClaude.Extras.ClaudeCode = "true"
	df = string(LayerDockerfile("img", &withClaude))
	if !strings.Contains(df, "/opt/pi/shims/npm install -g --prefix /opt/claude @anthropic-ai/claude-code@") || !strings.Contains(df, "/usr/local/bin/claude") || strings.Contains(df, "apt-get") {
		t.Errorf("claude layer:\n%s", df)
	}
}

func TestOtherPiVersionNeedsLocalBuild(t *testing.T) {
	c := &config.Config{}
	if !UsesPublished(c) {
		t.Error("the pinned Pi should use the published image")
	}
	c.Pi.Version = "9.9.9"
	if UsesPublished(c) {
		t.Error("a different Pi version must build locally")
	}
}
