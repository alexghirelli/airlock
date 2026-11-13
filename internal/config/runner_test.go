package config

import (
	"strings"
	"testing"
)

const goodCompose = "[[runner]]\nname = \"mw\"\ncompose = \"/srv/mw/docker-compose.yml\"\nservice = \"mediawiki\"\nmount = \"/var/www/html/w\"\n"

func TestRunnerValidationRejects(t *testing.T) {
	cases := map[string]string{
		"name upper case":       "[[runner]]\nname=\"MW\"\ncontainer=\"c\"\nmount=\"/w\"\n",
		"name with space":       "[[runner]]\nname=\"m w\"\ncontainer=\"c\"\nmount=\"/w\"\n",
		"no name":               "[[runner]]\ncontainer=\"c\"\nmount=\"/w\"\n",
		"duplicate":             goodCompose + "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/w\"\n",
		"compose no service":    "[[runner]]\nname=\"mw\"\ncompose=\"/c.yml\"\nmount=\"/w\"\n",
		"service no compose":    "[[runner]]\nname=\"mw\"\nservice=\"web\"\nmount=\"/w\"\n",
		"compose and container": "[[runner]]\nname=\"mw\"\ncompose=\"/c.yml\"\nservice=\"web\"\ncontainer=\"c\"\nmount=\"/w\"\n",
		"neither":               "[[runner]]\nname=\"mw\"\nmount=\"/w\"\n",
		"compose relative":      "[[runner]]\nname=\"mw\"\ncompose=\"docker-compose.yml\"\nservice=\"web\"\nmount=\"/w\"\n",
		"service with space":    "[[runner]]\nname=\"mw\"\ncompose=\"/c.yml\"\nservice=\"my web\"\nmount=\"/w\"\n",
		"service leading -":     "[[runner]]\nname=\"mw\"\ncompose=\"/c.yml\"\nservice=\"-web\"\nmount=\"/w\"\n",
		"container with space":  "[[runner]]\nname=\"mw\"\ncontainer=\"my c\"\nmount=\"/w\"\n",
		"container leading -":   "[[runner]]\nname=\"mw\"\ncontainer=\"-c\"\nmount=\"/w\"\n",
		"mount missing":         "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\n",
		"mount relative":        "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"var/www\"\n",
		"mount dotdot":          "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/var/www/../etc\"\n",
		"mount root":            "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/\"\n",
		"mount newline":         "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/a\\nb\"\n",
		"user bad":              "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/w\"\nuser=\"501 20\"\n",
		"user three parts":      "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/w\"\nuser=\"a:b:c\"\n",
		"unknown key":           "[[runner]]\nname=\"mw\"\ncontainer=\"c\"\nmount=\"/w\"\nshell=\"sh\"\n",
	}
	for name, cfg := range cases {
		if _, err := load(t, cfg, "", nil); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRunnerValidConfigs(t *testing.T) {
	cfg := goodCompose +
		"[[runner]]\nname=\"ddev\"\ncontainer=\"ddev-web\"\nmount=\"/var/www/html\"\nuser=\"501:20\"\n" +
		"[[runner]]\nname=\"home\"\ncompose=\"~/Sites/wmf/mw-core/docker-compose.yml\"\nservice=\"mediawiki\"\nmount=\"/var/www/html/w\"\nuser=\"www-data\"\n"
	r, err := load(t, cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Runners) != 3 {
		t.Fatalf("runners = %+v", r.Config.Runners)
	}
	if got := r.Config.Runners[0]; got.Compose != "/srv/mw/docker-compose.yml" || got.Service != "mediawiki" || got.Mount != "/var/www/html/w" {
		t.Errorf("compose runner decoded as %+v", got)
	}
	if got := r.Config.Runners[1]; got.Container != "ddev-web" || got.User != "501:20" {
		t.Errorf("container runner decoded as %+v", got)
	}
	// ~ is expanded when the runner is used; the stored value stays as written.
	if got := r.Config.Runners[2].Compose; got != "~/Sites/wmf/mw-core/docker-compose.yml" {
		t.Errorf("compose = %q", got)
	}
}

// A runner in a project's .airlock.toml is ignored and reported, like any other
// key a project may not set: the project may not start containers on the host.
func TestProjectConfigCannotDefineRunner(t *testing.T) {
	project := "[[runner]]\nname = \"evil\"\ncontainer = \"victim\"\nmount = \"/\"\n"
	r, err := load(t, "", project, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Runners) != 0 {
		t.Errorf("project config added runners: %+v", r.Config.Runners)
	}
	if joined := strings.Join(r.Warnings, "\n"); !strings.Contains(joined, "ignored runner") {
		t.Errorf("warnings do not report the runner:\n%s", joined)
	}
}

func TestRunnerNotSettableByEnvironment(t *testing.T) {
	r, err := load(t, goodCompose, "", map[string]string{
		"AIRLOCK_RUNNER_MW_CONTAINER": "x",
		"AIRLOCK_RUNNER":              "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Runners) != 1 || r.Config.Runners[0].Container != "" {
		t.Errorf("environment changed the runners: %+v", r.Config.Runners)
	}
}
