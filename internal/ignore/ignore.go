// Package ignore matches paths against gitignore-syntax patterns.
//
// Supported: comments, blank lines, negation (!), anchoring (a slash at the
// start or in the middle), directory-only patterns (trailing slash), *, ?, [..]
// classes and ** spanning directories. Escapes: a leading \# or \!.
package ignore

import (
	"bufio"
	"io"
	"path"
	"regexp"
	"strings"
)

// Matcher is an ordered list of patterns; the last matching pattern wins.
type Matcher struct {
	rules []rule
}

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// New compiles the patterns in r.
func New(r io.Reader) (*Matcher, error) {
	m := &Matcher{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if rl, ok := compile(sc.Text()); ok {
			m.rules = append(m.rules, rl)
		}
	}
	return m, sc.Err()
}

// FromLines compiles pattern lines.
func FromLines(lines ...string) *Matcher {
	m := &Matcher{}
	for _, l := range lines {
		if rl, ok := compile(l); ok {
			m.rules = append(m.rules, rl)
		}
	}
	return m
}

func compile(line string) (rule, bool) {
	line = strings.TrimRight(line, "\r\n")
	// Trailing spaces are ignored unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false
	}
	var rl rule
	if strings.HasPrefix(line, "!") {
		rl.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		rl.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return rule{}, false
	}
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("^(?:.*/)?")
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '*':
			if i+1 < len(line) && line[i+1] == '*' {
				// "**/" matches zero or more directories; a trailing "/**" everything inside.
				switch {
				case strings.HasPrefix(line[i:], "**/"):
					b.WriteString("(?:.*/)?")
					i += 2
				case i+2 == len(line):
					b.WriteString(".*")
					i++
				default:
					b.WriteString("[^/]*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(line[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				break
			}
			class := line[i+1 : i+j]
			class = strings.Replace(class, "!", "^", 1)
			b.WriteString("[" + class + "]")
			i += j
		case '\\':
			if i+1 < len(line) {
				i++
				b.WriteString(regexp.QuoteMeta(string(line[i])))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	// A pattern naming a directory also matches everything inside it.
	b.WriteString("(?:/.*)?$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return rule{}, false
	}
	rl.re = re
	return rl, true
}

// Match reports whether the slash-separated relative path is ignored. isDir
// says whether it is a directory. A path is ignored when it, or any parent
// directory, matches; a later negation re-includes a path (but, as in git, not
// a file whose parent directory is excluded).
func (m *Matcher) Match(p string, isDir bool) bool {
	p = path.Clean(strings.TrimPrefix(p, "/"))
	if p == "." || p == "" {
		return false
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		dir := isDir || i < len(parts)-1
		if m.matchOne(sub, dir) {
			return true
		}
	}
	return false
}

func (m *Matcher) matchOne(p string, isDir bool) bool {
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		// The compiled regexp also accepts "<p>/anything"; here we test the
		// path itself, so require a full match without the directory tail.
		if matchExact(r.re, p) {
			ignored = !r.negate
		}
	}
	return ignored
}

func matchExact(re *regexp.Regexp, p string) bool {
	loc := re.FindStringIndex(p)
	return loc != nil
}
