package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alexghirelli/airlock/internal/config"
)

func specs(ps []Proposal) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Spec)
	}
	return out
}

func pins(ps []Pin) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Tool+"<-"+p.File)
	}
	return out
}

// equalNil compares slices, treating nil and empty as the same.
func equalNil[T any](a, b []T) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func TestScan(t *testing.T) {
	cases := []struct {
		name     string
		files    fstest.MapFS
		mise     []string
		apt      []string
		pinned   []string
		mcp      []string
		services []string
		agents   []string
	}{
		{name: "empty"},
		{
			name:  "node lower bound only takes the newer of bound and default",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"engines":{"node":">=20"}}`)}},
			mise:  []string{"node@22"},
		},
		{
			name:  "node caret and x-range",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"engines":{"node":"^18.12"}}`)}},
			mise:  []string{"node@18"},
		},
		{
			name:  "node upper bound the default does not meet",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"engines":{"node":">=18 <21"}}`)}},
			mise:  []string{"node@18"},
		},
		{
			name:  "node alternatives",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"engines":{"node":"18 || 20"}}`)}},
			mise:  []string{"node@18"},
		},
		{
			name:  "node range the default meets",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"engines":{"node":">=18 <=22"}}`)}},
			mise:  []string{"node@22"},
		},
		{
			name:  "node tilde",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"engines":{"node":"~20.1"}}`)}},
			mise:  []string{"node@20"},
		},
		{
			name:  "node without engines",
			files: fstest.MapFS{"package.json": {Data: []byte(`{"name":"x"}`)}},
			mise:  []string{"node@22"},
		},
		{
			name:   "nvmrc pins node",
			files:  fstest.MapFS{"package.json": {Data: []byte(`{}`)}, ".nvmrc": {Data: []byte("20\n")}},
			pinned: []string{"node<-.nvmrc"},
		},
		{
			name:   "tool-versions pins go",
			files:  fstest.MapFS{"go.mod": {Data: []byte("module x\n\ngo 1.27.3\n")}, ".tool-versions": {Data: []byte("go 1.22.1\n")}},
			pinned: []string{"go<-.tool-versions"},
		},
		{
			name:   "mise.toml pins node",
			files:  fstest.MapFS{"package.json": {Data: []byte(`{}`)}, "mise.toml": {Data: []byte("[tools]\nnode = \"22\"\n")}},
			pinned: []string{"node<-mise.toml"},
		},
		{
			name:  "go directive",
			files: fstest.MapFS{"go.mod": {Data: []byte("module x\n\ngo 1.27.3\n")}},
			mise:  []string{"go@1.27.3"},
		},
		{
			name:  "python from requires-python",
			files: fstest.MapFS{"pyproject.toml": {Data: []byte("[project]\nrequires-python = \">=3.11\"\n")}},
			mise:  []string{"python@3.11"},
		},
		{
			name:  "python from requirements.txt",
			files: fstest.MapFS{"requirements.txt": {Data: []byte("requests\n")}},
			mise:  []string{"python@3.12"},
		},
		{
			name: "python-version pins python",
			files: fstest.MapFS{
				"requirements.txt": {Data: []byte("requests\n")},
				".python-version":  {Data: []byte("3.13\n")},
			},
			pinned: []string{"python<-.python-version"},
		},
		{
			name:  "ruby version from Gemfile",
			files: fstest.MapFS{"Gemfile": {Data: []byte("source 'https://rubygems.org'\nruby \"3.3.0\"\n")}},
			mise:  []string{"ruby@3.3.0"},
		},
		{
			name:  "rust",
			files: fstest.MapFS{"Cargo.toml": {Data: []byte("[package]\n")}},
			mise:  []string{"rust@latest"},
		},
		{
			name:  "php from composer",
			files: fstest.MapFS{"composer.json": {Data: []byte(`{"name":"acme/app"}`)}},
			apt:   []string{"php-cli", "php-mbstring", "php-xml", "php-intl", "php-curl", "php-zip", "composer"},
		},
		{
			name:  "mediawiki adds drivers",
			files: fstest.MapFS{"composer.json": {Data: []byte(`{"name":"mediawiki/core"}`)}},
			apt:   []string{"php-cli", "php-mbstring", "php-xml", "php-intl", "php-curl", "php-zip", "composer", "php-mysql", "php-sqlite3"},
		},
		{
			name:  "invalid mediawiki composer.json is still PHP, without the drivers",
			files: fstest.MapFS{"composer.json": {Data: []byte(`{"name": "mediawiki/core"`)}},
			apt:   []string{"php-cli", "php-mbstring", "php-xml", "php-intl", "php-curl", "php-zip", "composer"},
		},
		{
			name: "compose services in root and one level down",
			files: fstest.MapFS{
				"compose.yaml":              {Data: []byte("services:\n  db:\n    image: mariadb:11\n  web:\n    image: nginx:1\n  # image: redis:7\n")},
				"docker/docker-compose.yml": {Data: []byte("services:\n  cache:\n    image: \"docker.io/library/redis:7\"\n  reg:\n    image: localhost:5000/postgres:16\n")},
			},
			services: []string{"compose.yaml:mariadb:11", "docker/docker-compose.yml:docker.io/library/redis:7", "docker/docker-compose.yml:localhost:5000/postgres:16"},
		},
		{
			name:     "compose in .devcontainer",
			files:    fstest.MapFS{".devcontainer/compose.yml": {Data: []byte("services:\n  db:\n    image: postgres:16\n")}},
			services: []string{".devcontainer/compose.yml:postgres:16"},
		},
		{
			name: "compose images matched by name fragment",
			files: fstest.MapFS{"compose.yaml": {Data: []byte("services:\n" +
				"  a:\n    image: bitnami/postgresql:16\n" +
				"  b:\n    image: postgis/postgis:16\n" +
				"  c:\n    image: mysql/mysql-server:8\n" +
				"  d:\n    image: mongo:7\n" +
				"  e:\n    image: valkey/valkey:8\n" +
				"  f:\n    image: rabbitmq:3\n" +
				"  g:\n    image: nginx:1\n")}},
			services: []string{
				"compose.yaml:bitnami/postgresql:16", "compose.yaml:postgis/postgis:16",
				"compose.yaml:mysql/mysql-server:8", "compose.yaml:mongo:7",
				"compose.yaml:valkey/valkey:8", "compose.yaml:rabbitmq:3",
			},
		},
		{
			name:  "mcp files",
			files: fstest.MapFS{".mcp.json": {Data: []byte("{}")}, ".cursor/mcp.json": {Data: []byte("{}")}},
			mcp:   []string{".mcp.json", ".cursor/mcp.json"},
		},
		{
			name:   "agent instructions",
			files:  fstest.MapFS{"CLAUDE.md": {Data: []byte("# hi\n")}, "AGENTS.md": {Data: []byte("# hi\n")}},
			agents: []string{"AGENTS.md", "CLAUDE.md"},
		},
		{
			name:  "go.mod with a trailing comment and CRLF",
			files: fstest.MapFS{"go.mod": {Data: []byte("module x\r\n\r\ngo 1.27.3 // minimum\r\n")}},
			mise:  []string{"go@1.27.3"},
		},
		{
			name: "tool-versions aliases",
			files: fstest.MapFS{
				"package.json":   {Data: []byte(`{}`)},
				"go.mod":         {Data: []byte("go 1.22\n")},
				".tool-versions": {Data: []byte("# comment\nnodejs 20.11.0\ngolang 1.22.1\n")},
			},
			pinned: []string{"node<-.tool-versions", "go<-.tool-versions"},
		},
		{
			name: "a mise task named like a tool is not a pin",
			files: fstest.MapFS{
				"go.mod":    {Data: []byte("go 1.27.3\n")},
				"mise.toml": {Data: []byte("[tasks.build]\nrun = \"go build ./...\"\n")},
			},
			mise: []string{"go@1.27.3"},
		},
		{
			name: "mise config in .config pins python",
			files: fstest.MapFS{
				"requirements.txt":         {Data: []byte("x\n")},
				".config/mise/config.toml": {Data: []byte("[tools]\npython = \"3.13\"\n")},
			},
			pinned: []string{"python<-.config/mise/config.toml"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Scan(c.files)
			if got := specs(f.Mise); !equalNil(got, c.mise) {
				t.Errorf("mise = %v, want %v", got, c.mise)
			}
			if got := specs(f.Apt); !equalNil(got, c.apt) {
				t.Errorf("apt = %v, want %v", got, c.apt)
			}
			if got := pins(f.Pinned); !equalNil(got, c.pinned) {
				t.Errorf("pinned = %v, want %v", got, c.pinned)
			}
			if !equalNil(f.MCP, c.mcp) {
				t.Errorf("mcp = %v, want %v", f.MCP, c.mcp)
			}
			var svc []string
			for _, s := range f.Services {
				svc = append(svc, s.File+":"+s.Image)
			}
			if !equalNil(svc, c.services) {
				t.Errorf("services = %v, want %v", svc, c.services)
			}
			if !equalNil(f.Agents, c.agents) {
				t.Errorf("agents = %v, want %v", f.Agents, c.agents)
			}
			if c.name == "empty" && !f.Empty() {
				t.Errorf("empty project: %+v", f)
			}
		})
	}
}

func TestChooseNode(t *testing.T) {
	cases := []struct {
		raw   string
		want  int
		note  bool
		fails bool
	}{
		{raw: ">=20", want: 22},
		{raw: ">=24", want: 24},
		{raw: ">20", want: 22},
		{raw: "^18.12", want: 18},
		{raw: "~20.1", want: 20},
		{raw: "=20", want: 20},
		{raw: "20.x", want: 20},
		{raw: "20", want: 20},
		{raw: "v20", want: 20},
		{raw: ">=18 <21", want: 18, note: true},
		{raw: ">= 18 <21", want: 18, note: true},
		{raw: "18 || 20", want: 18, note: true},
		{raw: ">=18 <=22", want: 22},
		{raw: ">=16 || ^20", want: 22},
		{raw: "<21", fails: true},
		{raw: "latest", fails: true},
		{raw: "*", fails: true},
		{raw: "18 ||", fails: true},
	}
	for _, c := range cases {
		got, note, ok := chooseNode(c.raw)
		if c.fails {
			if ok {
				t.Errorf("chooseNode(%q) = %d, want it refused", c.raw, got)
			}
			continue
		}
		if !ok || got != c.want || (note != "") != c.note {
			t.Errorf("chooseNode(%q) = %d, %q, %v; want %d, note %v", c.raw, got, note, ok, c.want, c.note)
		}
	}
}

func TestPinBeatsSourceFile(t *testing.T) {
	// A pin in a file mise reads wins over the source file; the first source per tool decides.
	f := Scan(fstest.MapFS{
		"package.json":     {Data: []byte(`{"engines":{"node":">=20"}}`)},
		".node-version":    {Data: []byte("18\n")},
		"requirements.txt": {Data: []byte("x\n")},
		"pyproject.toml":   {Data: []byte("[project]\nrequires-python = \">=3.11\"\n")},
	})
	if got := pins(f.Pinned); !reflect.DeepEqual(got, []string{"node<-.node-version"}) {
		t.Errorf("pinned = %v", got)
	}
	if got := specs(f.Mise); !reflect.DeepEqual(got, []string{"python@3.11"}) {
		t.Errorf("mise = %v", got)
	}
}

// TestManyRootEntries: the subfolder bound counts folders searched, not root
// entries, so a compose file in a folder that sorts late is still found.
func TestManyRootEntries(t *testing.T) {
	files := fstest.MapFS{"docker/compose.yml": {Data: []byte("services:\n  db:\n    image: mariadb:11\n")}}
	for i := 0; i < 150; i++ {
		files[fmt.Sprintf("a%03d.txt", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	f := Scan(files)
	if len(f.Services) != 1 || f.Services[0].File != "docker/compose.yml" {
		t.Errorf("services = %+v", f.Services)
	}
}

func TestImageName(t *testing.T) {
	for in, want := range map[string]string{
		"mariadb:11":                    "mariadb",
		"docker.io/library/postgres:16": "postgres",
		"localhost:5000/redis@sha256:x": "redis",
		"bitnami/memcached":             "memcached",
	} {
		if got := imageName(in); got != want {
			t.Errorf("imageName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIgnoreLine(t *testing.T) {
	for in, want := range map[string]string{
		"config/.env": "config/.env",
		"#notes":      `\#notes`,
		"!keep":       `\!keep`,
		"a*b.pem":     `a\*b.pem`,
		"key[1].pem":  `key\[1\].pem`,
		"back\\slash": `back\\slash`,
		"trail ":      `trail\ `,
	} {
		if got := IgnoreLine(in); got != want {
			t.Errorf("IgnoreLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderLoads feeds the rendered file to the real project loader: it must
// parse, set nothing the loader ignores, and carry exactly the proposed packages.
func TestRenderLoads(t *testing.T) {
	stacks := []struct {
		name  string
		files fstest.MapFS
	}{
		{"node and go", fstest.MapFS{
			"package.json": {Data: []byte(`{"engines":{"node":">=20"}}`)},
			"go.mod":       {Data: []byte("module x\n\ngo 1.27.3\n")},
		}},
		{"node with an upper bound", fstest.MapFS{
			"package.json": {Data: []byte(`{"engines":{"node":"18 || 20"}}`)},
		}},
		{"python and rust", fstest.MapFS{
			"pyproject.toml": {Data: []byte("[project]\nrequires-python = \">=3.11\"\n")},
			"Cargo.toml":     {Data: []byte("[package]\n")},
		}},
		{"mediawiki", fstest.MapFS{
			"composer.json": {Data: []byte(`{"name":"mediawiki/core"}`)},
		}},
		{"ruby with a pinned node", fstest.MapFS{
			"Gemfile": {Data: []byte("ruby \"3.3.0\"\n")},
			".nvmrc":  {Data: []byte("22\n")},
		}},
	}
	for _, s := range stacks {
		t.Run(s.name, func(t *testing.T) {
			f := Scan(s.files)
			text := Render(f)
			if text == "" {
				t.Fatal("nothing rendered")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, config.ProjectConfigName), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := config.Load(config.LoadOptions{
				ProjectDir:     dir,
				UserConfigPath: filepath.Join(dir, "no-user-config.toml"),
				Getenv:         func(string) string { return "" },
			})
			if err != nil {
				t.Fatalf("load: %v\n%s", err, text)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings: %v\n%s", res.Warnings, text)
			}
			if !equalNil(res.ProjectExtras.Mise, specs(f.Mise)) {
				t.Errorf("mise = %v, want %v", res.ProjectExtras.Mise, specs(f.Mise))
			}
			var apt []string
			for _, p := range f.Apt {
				apt = append(apt, p.Spec)
			}
			if !equalNil(res.ProjectExtras.Apt, apt) {
				t.Errorf("apt = %v, want %v", res.ProjectExtras.Apt, apt)
			}
		})
	}
}

func TestRenderNothingToSet(t *testing.T) {
	// Pinned versions and services need no project setting.
	f := Scan(fstest.MapFS{
		".nvmrc":      {Data: []byte("22\n")},
		"compose.yml": {Data: []byte("services:\n  db:\n    image: mysql:8\n")},
	})
	if text := Render(f); text != "" {
		t.Errorf("rendered %q for nothing to set", text)
	}
	if f.Empty() {
		t.Error("findings are not empty")
	}
}

func TestSuggestions(t *testing.T) {
	f := Findings{
		MCP:      []string{".mcp.json"},
		Services: []Service{{File: "compose.yaml", Image: "mariadb:11"}},
	}
	got := Suggestions(f, []string{".env", "id_rsa"}, false)
	if len(got) != 3 {
		t.Fatalf("suggestions = %q", got)
	}
	for i, want := range []string{"airlock mcp import .mcp.json", "mariadb:11", ".airlockignore"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("suggestion %d = %q, want it to mention %q", i, got[i], want)
		}
	}
	if strings.Contains(got[2], "and more") {
		t.Errorf("no cap, but %q says more", got[2])
	}
	capped := Suggestions(Findings{}, []string{".env"}, true)
	if len(capped) != 1 || !strings.HasSuffix(capped[0], "and more") {
		t.Errorf("capped suggestions = %q", capped)
	}
}
