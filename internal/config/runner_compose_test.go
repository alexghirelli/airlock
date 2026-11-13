package config

import (
	"reflect"
	"testing"
)

func TestRunnerComposeAcceptsAListInOrder(t *testing.T) {
	cfg := "[[runner]]\nname = \"mw\"\ncompose = [\"/srv/mw/docker-compose.yml\", \"/srv/mw/docker-compose.override.yml\"]\nservice = \"mediawiki\"\nmount = \"/w\"\n"
	r, err := load(t, cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.Config.Runners[0].ComposeFiles()
	if err != nil || !reflect.DeepEqual(files, []string{"/srv/mw/docker-compose.yml", "/srv/mw/docker-compose.override.yml"}) {
		t.Errorf("files = %q, %v", files, err)
	}
}

func TestRunnerComposeListIsValidatedEntryByEntry(t *testing.T) {
	bad := map[string]string{
		"relative entry":  "compose = [\"/ok.yml\", \"rel.yml\"]",
		"empty entry":     "compose = [\"\"]",
		"number entry":    "compose = [1]",
		"not a list/path": "compose = 3",
		"empty list":      "compose = []",
	}
	for name, line := range bad {
		cfg := "[[runner]]\nname = \"mw\"\n" + line + "\nservice = \"mediawiki\"\nmount = \"/w\"\n"
		if _, err := load(t, cfg, "", nil); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestRunnerComposeFilesAsWritten(t *testing.T) {
	if files, err := (Runner{Compose: "~/x.yml"}).ComposeFiles(); err != nil || !reflect.DeepEqual(files, []string{"~/x.yml"}) {
		t.Errorf("string form: %q, %v", files, err)
	}
	if files, err := (Runner{}).ComposeFiles(); err != nil || files != nil {
		t.Errorf("unset: %q, %v", files, err)
	}
}
