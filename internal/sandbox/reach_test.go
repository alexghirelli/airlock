package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/config"
)

// A project whose folder holds the airlock configuration folder is not started:
// the sandbox would see, and could rewrite, the trusted settings.
func TestBuildPlanRefusesAProjectThatReachesTheSettings(t *testing.T) {
	base := realTemp(t)
	xdg := filepath.Join(base, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg := filepath.Join(xdg, "airlock", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{UserConfigPath: cfg, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	host := Host{UID: 1, GID: 1, Getenv: func(string) string { return "" },
		GitConfig: func(*Project, string) string { return "" }}

	// The project is the folder that holds the config folder.
	parent, err := Resolve(mkRepo(t, xdg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildPlan(PlanInput{Project: parent, Config: res, Image: "airlock:test", Host: host})
	if err == nil || !strings.Contains(err.Error(), "reaches") {
		t.Fatalf("a project that holds the settings must not start: %v", err)
	}

	// A project beside the config folder starts.
	legit, err := Resolve(mkRepo(t, filepath.Join(base, "Sites", "mw-core")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(PlanInput{Project: legit, Config: res, Image: "airlock:test", Host: host}); err != nil {
		t.Fatalf("a legitimate project must start: %v", err)
	}
}
