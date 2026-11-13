package summary

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The parsers read git's -z output, where paths are NUL-terminated and never
// quoted, so a space or a newline in a file name is just bytes.

// parseNumstat reads `git diff --numstat -z`. A record is "added\tdeleted\tpath";
// a rename or copy has an empty path and is followed by the old and new paths.
// Binary files show "-" for both counts.
func parseNumstat(out string) ([]File, error) {
	toks := strings.Split(out, "\x00")
	var files []File
	for i := 0; i < len(toks); i++ {
		if toks[i] == "" {
			continue
		}
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected numstat record %q", toks[i])
		}
		file := File{Path: f[2]}
		if f[2] == "" {
			if i+2 >= len(toks) {
				return nil, errors.New("numstat: rename without its paths")
			}
			file.OldPath, file.Path = toks[i+1], toks[i+2]
			i += 2
		}
		if f[0] == "-" && f[1] == "-" {
			file.Binary = true
		} else {
			added, err1 := strconv.Atoi(f[0])
			deleted, err2 := strconv.Atoi(f[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("unexpected numstat counts in %q", toks[i])
			}
			file.Added, file.Deleted = added, deleted
		}
		files = append(files, file)
	}
	return files, nil
}

// parseNameStatus reads `git diff --name-status -z` into the status letter of
// each file, keyed by its (new) path. Renames and copies carry two paths.
func parseNameStatus(out string) map[string]string {
	toks := strings.Split(out, "\x00")
	status := map[string]string{}
	for i := 0; i < len(toks); i++ {
		code := toks[i]
		if code == "" {
			continue
		}
		if (code[0] == 'R' || code[0] == 'C') && i+2 < len(toks) {
			status[toks[i+2]] = code[:1]
			i += 2
			continue
		}
		if i+1 >= len(toks) {
			break
		}
		status[toks[i+1]] = code[:1]
		i++
	}
	return status
}

// countStatus counts the entries of `git status --porcelain=v1 -z` that are
// changes to tracked files. A rename is followed by its old path, which is not
// an entry of its own.
func countStatus(out string) int {
	toks := strings.Split(out, "\x00")
	n := 0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 { // "XY p": the empty token after the last NUL is shorter
			continue
		}
		if t[:2] == "??" || t[:2] == "!!" {
			continue
		}
		n++
		if strings.ContainsAny(t[:2], "RC") {
			i++
		}
	}
	return n
}

// parseLog reads `git log --format=%h%x1f%s`, one commit per line.
func parseLog(out string) []Commit {
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		hash, subject, ok := strings.Cut(line, "\x1f")
		if !ok || hash == "" {
			continue
		}
		commits = append(commits, Commit{Hash: hash, Subject: subject})
	}
	return commits
}

// splitNUL splits -z output into its non-empty entries.
func splitNUL(out string) []string {
	var list []string
	for _, s := range strings.Split(out, "\x00") {
		if s != "" {
			list = append(list, s)
		}
	}
	return list
}
