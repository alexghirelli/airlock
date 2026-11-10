package ignore

import "testing"

func TestMatch(t *testing.T) {
	m := FromLines(
		"# comment",
		"",
		".env*",
		"*.pem",
		"/secrets.txt",
		"config/local/",
		"**/credentials.json",
		"logs/**",
		"!.env.example",
		"build",
		`\#notacomment`,
		"data?.db",
		"keys[0-9].txt",
	)
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{".env", false, true},
		{".env.local", false, true},
		{"sub/dir/.env", false, true},
		{".env.example", false, false}, // re-included
		{"cert.pem", false, true},
		{"deep/er/cert.pem", false, true},
		{"cert.pem.txt", false, false},
		{"secrets.txt", false, true},
		{"sub/secrets.txt", false, false}, // anchored
		{"config/local", true, true},
		{"config/local/a.txt", false, true}, // inside an ignored dir
		{"config/local", false, false},      // dir-only pattern vs a file
		{"config/other.txt", false, false},
		{"credentials.json", false, true},
		{"a/b/c/credentials.json", false, true},
		{"logs/x.log", false, true},
		{"logs", true, false},
		{"build/out.o", false, true},
		{"src/build", true, true},
		{"#notacomment", false, true},
		{"data1.db", false, true},
		{"data12.db", false, false},
		{"keys7.txt", false, true},
		{"keysa.txt", false, false},
		{"README.md", false, false},
		{".", true, false},
	}
	for _, c := range cases {
		if got := m.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", c.path, c.isDir, got, c.want)
		}
	}
}

func TestEmpty(t *testing.T) {
	m := FromLines()
	if m.Match("anything", false) {
		t.Error("empty matcher matched")
	}
}
