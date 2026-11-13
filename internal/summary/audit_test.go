package summary

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditStates(t *testing.T) {
	cases := []struct {
		name  string
		audit *Audit
		line  string // the text line, "" for none
		md    string // a substring of the Markdown
	}{
		{"not read", nil, "", ""},
		{"no baseline", &Audit{Baseline: false}, "audit: no baseline yet (airlock audit --accept records one)", "No baseline yet: `airlock audit --accept` records one."},
		{"baseline, no changes", &Audit{Baseline: true, Changes: []string{}}, "", "No changes to host-executable files since the last review."},
		{"one change", &Audit{Baseline: true, Changes: []string{"added .envrc"}}, "audit: 1 change to host-executable files, project-wide (airlock audit)", "- `added .envrc`"},
	}
	for _, c := range cases {
		s := &Summary{Name: "n", Head: "abc", Audit: c.audit}
		text := s.Text()
		if c.line == "" {
			if strings.Contains(text, "audit:") {
				t.Errorf("%s: unexpected audit line:\n%s", c.name, text)
			}
		} else if !strings.Contains(text, c.line+"\n") {
			t.Errorf("%s: text lacks %q:\n%s", c.name, c.line, text)
		}
		md := s.Markdown()
		if c.md == "" {
			if strings.Contains(md, "Project audit") {
				t.Errorf("%s: unexpected audit section:\n%s", c.name, md)
			}
		} else if !strings.Contains(md, c.md) {
			t.Errorf("%s: markdown lacks %q:\n%s", c.name, c.md, md)
		}
	}
}

func TestAuditJSONIsNullWhenNotRead(t *testing.T) {
	data, err := json.Marshal(&Summary{Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"audit":null`) {
		t.Errorf("audit is not null when not read: %s", data)
	}
}
