// Package detect looks at a project and proposes the starter .airlock.toml that
// `airlock init` writes. Scan is pure: it reads a fixed set of known files from
// an fs.FS, never a whole tree, so its work is bounded.
package detect

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/alexghirelli/airlock/internal/config"
)

// Findings is what Scan found in a project root.
type Findings struct {
	// Pinned are toolchains that a version file mise reads already pins: nothing to add.
	Pinned []Pin `json:"pinned,omitempty"`
	// Mise and Apt are what to add to extras.mise and extras.apt.
	Mise []Proposal `json:"mise,omitempty"`
	Apt  []Proposal `json:"apt,omitempty"`
	// MCP are MCP config files; `airlock mcp import` turns them into user config.
	MCP []string `json:"mcp,omitempty"`
	// Services are compose services that run a database or cache image.
	Services []Service `json:"services,omitempty"`
	// Agents are the agent instruction files in the root (AGENTS.md, CLAUDE.md).
	Agents []string `json:"agents,omitempty"`
	// Notes are facts worth showing that do not change the file.
	Notes []string `json:"notes,omitempty"`
}

// Pin is a toolchain that a version file already pins.
type Pin struct {
	Tool string `json:"tool"`
	File string `json:"file"`
}

// Proposal is one package to add to the project config, and why.
type Proposal struct {
	Spec string `json:"spec"`
	From string `json:"from"`
	Why  string `json:"why"`
}

// Service is a compose service whose image is a database or cache server.
type Service struct {
	File  string `json:"file"`
	Image string `json:"image"`
}

// Empty reports whether nothing was found.
func (f Findings) Empty() bool {
	return len(f.Pinned)+len(f.Mise)+len(f.Apt)+len(f.MCP)+len(f.Services)+len(f.Agents)+len(f.Notes) == 0
}

const (
	// maxFileSize bounds what Scan reads from one file: these are small manifests.
	maxFileSize = 1 << 20
	// maxSubdirs bounds how many subfolders are searched for compose files.
	maxSubdirs = 100
	// defaultNodeMajor and defaultPython are used when the project does not say.
	defaultNodeMajor = config.PinnedNodeMajor
	defaultPython    = "3.12"
)

// tomlMiseFiles are the mise config files that hold a [tools] table.
var tomlMiseFiles = []string{
	"mise.toml", ".mise.toml", "mise.local.toml", ".config/mise.toml", ".config/mise/config.toml",
}

// idiomatic are the one-tool version files mise reads in the sandbox
// (MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS in image/Dockerfile).
var idiomatic = []struct{ file, tool string }{
	{".nvmrc", "node"}, {".node-version", "node"},
	{".python-version", "python"}, {".ruby-version", "ruby"}, {".go-version", "go"},
}

// toolNames are the toolchains airlock can propose through extras.mise.
var toolNames = []string{"node", "python", "ruby", "go", "rust"}

// toolAliases map the names asdf and mise accept for a tool to the mise name.
var toolAliases = map[string]string{"nodejs": "node", "golang": "go"}

// composeNames are the compose file names, in the root and in subfolders.
var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// serverImages are name fragments of images whose service needs a server airlock
// does not run yet. A name matches when it contains one (bitnami/postgresql,
// mysql/mysql-server), so an exporter or client image with such a name matches too.
var serverImages = []string{
	"mariadb", "mysql", "postgres", "postgis", "redis", "valkey", "memcached",
	"mongo", "elasticsearch", "opensearch", "rabbitmq",
}

// phpPackages are the Debian packages for PHP. mise compiles PHP from source,
// which takes too long for a sandbox start, so PHP comes from apt.
var phpPackages = []struct{ pkg, why string }{
	{"php-cli", "composer.json: PHP runtime from Debian (mise would compile it from source)"},
	{"php-mbstring", "composer.json: PHP extension"},
	{"php-xml", "composer.json: PHP extension"},
	{"php-intl", "composer.json: PHP extension"},
	{"php-curl", "composer.json: PHP extension"},
	{"php-zip", "composer.json: PHP extension"},
	{"composer", "composer.json: the Composer package manager"},
}

var (
	pyRangeRe     = regexp.MustCompile(`^>=\s*(\d+\.\d+)(?:\.\d+)?\s*(?:,\s*<[^,]+)?$`)
	goDirectiveRe = regexp.MustCompile(`(?m)^go\s+(\d+(?:\.\d+)+)[ \t]*(?://[^\n]*)?\r?$`)
	rubyRe        = regexp.MustCompile(`(?m)^\s*ruby\s+["'](\d+\.\d+(?:\.\d+)?)["']`)
	imageRe       = regexp.MustCompile(`(?m)^\s*image:\s*["']?([^"'\s#]+)`)
)

// Scan looks at the project root fsys and returns what it uses.
func Scan(fsys fs.FS) Findings {
	s := &scan{fsys: fsys, pins: map[string]string{}, done: map[string]bool{}}
	s.readPins()
	s.toolchains()
	s.composer()
	s.compose()
	s.mcp()
	s.agents()
	return s.f
}

type scan struct {
	fsys fs.FS
	f    Findings
	pins map[string]string // tool -> the file mise reads that pins it
	done map[string]bool   // tool -> already decided
}

// read returns a small regular file's content. Anything else (a directory, a
// symlink to a device, a FIFO) is refused before it is opened, because opening a
// FIFO blocks and reading /dev/zero never ends. The check after the open covers
// a file replaced in between.
func (s *scan) read(name string) (string, bool) {
	if !s.exists(name) {
		return "", false
	}
	f, err := s.fsys.Open(name)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil || len(b) > maxFileSize {
		return "", false
	}
	return string(b), true
}

// exists reports whether name is a regular file, following symlinks. A symlink
// to a device or a FIFO is not a file to read.
func (s *scan) exists(name string) bool {
	// Lstat: a symlink is not followed, so a cloned repository cannot point a
	// manifest at a file outside the project and have it read.
	st, err := fs.Lstat(s.fsys, name)
	return err == nil && st.Mode().IsRegular()
}

func (s *scan) pin(tool, file string) {
	if _, ok := s.pins[tool]; !ok {
		s.pins[tool] = file
	}
}

func (s *scan) readPins() {
	if text, ok := s.read(".tool-versions"); ok {
		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
				continue
			}
			s.pinIfTool(fields[0], ".tool-versions")
		}
	}
	for _, name := range tomlMiseFiles {
		text, ok := s.read(name)
		if !ok {
			continue
		}
		var doc struct {
			Tools map[string]any `toml:"tools"`
		}
		if toml.Unmarshal([]byte(text), &doc) != nil {
			continue // not a mise file we can read; nothing is pinned by it
		}
		for tool := range doc.Tools {
			s.pinIfTool(tool, name)
		}
	}
	for _, id := range idiomatic {
		if s.exists(id.file) {
			s.pin(id.tool, id.file)
		}
	}
}

// pinIfTool pins tool (after alias mapping) when it is one airlock proposes.
func (s *scan) pinIfTool(tool, file string) {
	if c, ok := toolAliases[tool]; ok {
		tool = c
	}
	for _, t := range toolNames {
		if t == tool {
			s.pin(tool, file)
		}
	}
}

// use records a toolchain the project uses. The first source decides it: a
// version file mise reads pins it (nothing to add), otherwise it is proposed.
func (s *scan) use(tool, from, spec, why string) {
	if s.done[tool] {
		return
	}
	s.done[tool] = true
	if file, ok := s.pins[tool]; ok {
		s.f.Pinned = append(s.f.Pinned, Pin{Tool: tool, File: file})
		return
	}
	s.f.Mise = append(s.f.Mise, Proposal{Spec: spec, From: from, Why: why})
}

func (s *scan) toolchains() {
	if text, ok := s.read("package.json"); ok {
		major, why := nodeMajor(text)
		s.use("node", "package.json", "node@"+major, why)
	}
	if text, ok := s.read("go.mod"); ok {
		if m := goDirectiveRe.FindStringSubmatch(text); m != nil {
			s.use("go", "go.mod", "go@"+m[1], "go.mod: go "+m[1])
		} else {
			s.use("go", "go.mod", "go@latest", "go.mod: no go directive")
		}
	}
	for _, file := range []string{"pyproject.toml", "requirements.txt", "setup.py"} {
		if text, ok := s.read(file); ok {
			ver, why := pythonVersion(file, text)
			s.use("python", file, "python@"+ver, why)
			break
		}
	}
	if text, ok := s.read("Gemfile"); ok {
		ver, why := "latest", "Gemfile: no ruby version line"
		if m := rubyRe.FindStringSubmatch(text); m != nil {
			ver, why = m[1], "Gemfile: ruby "+m[1]
		}
		s.use("ruby", "Gemfile", "ruby@"+ver, why)
	}
	for _, file := range []string{"Cargo.toml", "rust-toolchain.toml", "rust-toolchain"} {
		if s.exists(file) {
			s.use("rust", file, "rust@latest", file+": Rust toolchain")
			break
		}
	}
}

// nodeMajor returns the Node major to propose for package.json's engines.node.
func nodeMajor(packageJSON string) (major, why string) {
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if json.Unmarshal([]byte(packageJSON), &pkg) != nil || strings.TrimSpace(pkg.Engines.Node) == "" {
		return defaultNodeMajor, "package.json: no engines.node; airlock's default"
	}
	raw := strings.TrimSpace(pkg.Engines.Node)
	n, note, ok := chooseNode(raw)
	if !ok {
		return defaultNodeMajor, fmt.Sprintf("package.json: engines.node %q is not a simple range; airlock's default", raw)
	}
	why = fmt.Sprintf("package.json: engines.node %q", raw)
	if note != "" {
		why += "; " + note
	}
	return strconv.Itoa(n), why
}

// nodeComparator is one part of an engines.node range: an operator and a major.
type nodeComparator struct {
	op    string // "", "^", "~", "=" (exact major), or ">=", ">", "<=", "<"
	major int
}

var (
	nodeCompRe    = regexp.MustCompile(`^(>=|<=|>|<|\^|~|=)?v?(\d+)(?:\.(?:x|\*|\d+))*$`)
	nodeOpSpaceRe = regexp.MustCompile(`(>=|<=|>|<|\^|~|=)\s+`)
)

// parseNodeRange splits an engines.node range into "||" alternatives, each a list
// of comparators that must all hold. It reports false for anything else.
func parseNodeRange(raw string) ([][]nodeComparator, bool) {
	var groups [][]nodeComparator
	for _, alt := range strings.Split(raw, "||") {
		fields := strings.Fields(nodeOpSpaceRe.ReplaceAllString(alt, "$1"))
		if len(fields) == 0 {
			return nil, false
		}
		var g []nodeComparator
		for _, f := range fields {
			m := nodeCompRe.FindStringSubmatch(f)
			if m == nil {
				return nil, false
			}
			n, err := strconv.Atoi(m[2])
			if err != nil {
				return nil, false
			}
			g = append(g, nodeComparator{op: m[1], major: n})
		}
		groups = append(groups, g)
	}
	return groups, true
}

func (c nodeComparator) holds(m int) bool {
	switch c.op {
	case ">=":
		return m >= c.major
	case ">":
		return m > c.major
	case "<":
		return m < c.major
	case "<=":
		return m <= c.major
	}
	return m == c.major // exact: "", ^, ~, =
}

// lower returns the lowest major the comparator allows, if it sets one.
func (c nodeComparator) lower() (int, bool) {
	switch c.op {
	case ">=":
		return c.major, true
	case ">":
		return c.major + 1, true
	case "<", "<=":
		return 0, false
	}
	return c.major, true
}

func satisfies(m int, groups [][]nodeComparator) bool {
	for _, g := range groups {
		all := true
		for _, c := range g {
			if !c.holds(m) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// chooseNode picks the Node major for a range. A range with no upper bound gets
// the newer of its lower bound and airlock's default, never an end-of-life
// major just because it is allowed. An exact major is taken as written. A range
// with an upper bound keeps the default if the default satisfies it, else it
// takes the lowest allowed major and the caller notes it. ok is false for a
// range it cannot read.
func chooseNode(raw string) (major int, note string, ok bool) {
	groups, ok := parseNodeRange(raw)
	if !ok {
		return 0, "", false
	}
	def, _ := strconv.Atoi(defaultNodeMajor)
	if len(groups) == 1 {
		g := groups[0]
		lowerOnly := true
		low := 0
		for _, c := range g {
			if c.op != ">=" && c.op != ">" {
				lowerOnly = false
				break
			}
			if l, _ := c.lower(); l > low {
				low = l
			}
		}
		if lowerOnly {
			return max(low, def), "", true
		}
		if len(g) == 1 && g[0].op != "<" && g[0].op != "<=" && g[0].op != ">=" && g[0].op != ">" {
			return g[0].major, "", true
		}
	}
	if satisfies(def, groups) {
		return def, "", true
	}
	found := false
	low := 0
	for _, g := range groups {
		groupLow, has := 0, false
		for _, c := range g {
			if l, ok := c.lower(); ok && (!has || l > groupLow) {
				groupLow, has = l, true
			}
		}
		if has && (!found || groupLow < low) {
			low, found = groupLow, true
		}
	}
	if !found {
		return 0, "", false
	}
	return low, fmt.Sprintf("%d is outside the range, so the lowest allowed major %d is proposed; check it", def, low), true
}

// pythonVersion returns the lower bound of a simple requires-python, or the default.
func pythonVersion(file, text string) (ver, why string) {
	if file == "pyproject.toml" {
		var p struct {
			Project struct {
				RequiresPython string `toml:"requires-python"`
			} `toml:"project"`
		}
		if toml.Unmarshal([]byte(text), &p) == nil && strings.TrimSpace(p.Project.RequiresPython) != "" {
			raw := strings.TrimSpace(p.Project.RequiresPython)
			if m := pyRangeRe.FindStringSubmatch(raw); m != nil {
				return m[1], fmt.Sprintf("pyproject.toml: requires-python %q", raw)
			}
			return defaultPython, fmt.Sprintf("pyproject.toml: requires-python %q is not a simple lower bound; airlock's default", raw)
		}
	}
	return defaultPython, file + ": no requires-python; airlock's default"
}

// composer proposes PHP from apt. MediaWiki also needs a database driver.
func (s *scan) composer() {
	text, ok := s.read("composer.json")
	if !ok {
		return
	}
	var c struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(text), &c) // still PHP if the file is not valid JSON
	add := func(pkg, why string) {
		s.f.Apt = append(s.f.Apt, Proposal{Spec: pkg, From: "composer.json", Why: why})
	}
	for _, p := range phpPackages {
		add(p.pkg, p.why)
	}
	if c.Name == "mediawiki/core" {
		add("php-mysql", "composer.json: MediaWiki database driver")
		add("php-sqlite3", "composer.json: MediaWiki database driver (SQLite needs no server)")
		s.f.Notes = append(s.f.Notes,
			"MediaWiki needs a database. airlock runs no database containers yet: use SQLite, or a database on the host or LAN opened with network.allow_cidrs")
	}
}

// compose finds database and cache services in compose files in the root and in
// its immediate subfolders. The image lines are read without a YAML parser.
func (s *scan) compose() {
	var files []string
	for _, name := range composeNames {
		if s.exists(name) {
			files = append(files, name)
		}
	}
	if entries, err := fs.ReadDir(s.fsys, "."); err == nil {
		scanned := 0
		for _, e := range entries {
			if scanned >= maxSubdirs {
				break
			}
			if !e.IsDir() || skipDir(e.Name()) {
				continue
			}
			scanned++
			for _, name := range composeNames {
				if p := e.Name() + "/" + name; s.exists(p) {
					files = append(files, p)
				}
			}
		}
	}
	for _, file := range files {
		text, ok := s.read(file)
		if !ok {
			continue
		}
		for _, m := range imageRe.FindAllStringSubmatch(text, -1) {
			if isServer(m[1]) {
				s.f.Services = append(s.f.Services, Service{File: file, Image: m[1]})
			}
		}
	}
}

// skipDir reports whether compose files are not searched for in a folder.
// .devcontainer is the one hidden folder that commonly holds one.
func skipDir(name string) bool {
	if name == ".devcontainer" {
		return false
	}
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

// isServer reports whether an image's name contains the name of a server image.
func isServer(image string) bool {
	name := strings.ToLower(imageName(image))
	for _, s := range serverImages {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

// imageName returns the last path segment of an image, without tag or digest.
func imageName(image string) string {
	name := image[strings.LastIndex(image, "/")+1:]
	if i := strings.IndexAny(name, "@:"); i >= 0 {
		name = name[:i]
	}
	return name
}

func (s *scan) mcp() {
	for _, p := range []string{".mcp.json", ".cursor/mcp.json", ".vscode/mcp.json"} {
		if s.exists(p) {
			s.f.MCP = append(s.f.MCP, p)
		}
	}
}

func (s *scan) agents() {
	for _, p := range []string{"AGENTS.md", "CLAUDE.md"} {
		if s.exists(p) {
			s.f.Agents = append(s.f.Agents, p)
			s.f.Notes = append(s.f.Notes, p+" is read by the agents, inside the sandbox too")
		}
	}
}
