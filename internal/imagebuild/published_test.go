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
	if df := LayerDockerfile("img", plain); df != nil {
		t.Errorf("nothing to add, got a layer:\n%s", df)
	}
	// Agents never go into the image: Claude Code is installed at run time.
	plain.Agent.Kind = "claude"
	if df := LayerDockerfile("img", plain); df != nil {
		t.Errorf("an agent must not add an image layer:\n%s", df)
	}
	withApt := &config.Config{}
	withApt.Extras.Apt = []string{"ffmpeg", "tree"}
	df := string(LayerDockerfile("img", withApt))
	if !strings.HasPrefix(df, "FROM img\n") || !strings.Contains(df, "apt-get install -y --no-install-recommends ffmpeg tree") || strings.Contains(df, "claude") {
		t.Errorf("apt layer:\n%s", df)
	}
}

func TestBaseImageIsTheSameForEveryAgent(t *testing.T) {
	a, b := &config.Config{}, &config.Config{}
	b.Agent.Kind, b.Agent.Version = "claude", "latest"
	b.Pi.Version = "9.9.9"
	if BaseRef(a) != BaseRef(b) {
		t.Errorf("the agent or its version changed the image: %s vs %s", BaseRef(a), BaseRef(b))
	}
	for k, v := range BaseArgs(b) {
		if strings.Contains(strings.ToLower(k+v), "claude") || v == "9.9.9" {
			t.Errorf("build arg %s=%s", k, v)
		}
	}
}
