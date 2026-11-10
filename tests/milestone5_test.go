//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test 10: audit detects an added hook and a new package.json script.
func TestAuditDetectsHookAndScript(t *testing.T) {
	p := newProject(t, "aud", "")
	os.WriteFile(filepath.Join(p.Dir, "package.json"), []byte(`{"name":"x","scripts":{"test":"jest"}}`), 0o644)
	p.mustSh("true") // creates the sandbox and the baseline

	ctx, cancel := ctxMinutes(2)
	defer cancel()
	if out, _, _ := p.run(ctx, nil, "audit"); !strings.Contains(out, "no changes") {
		t.Fatalf("fresh sandbox should have no changes:\n%s", out)
	}

	// Hooks are read-only inside the box, so the "attack" happens through the
	// project files the agent can write: a new script, an .envrc and a new
	// executable. The hook is planted from the host side to prove detection.
	p.mustSh(`
sed -i 's/"test":"jest"/"test":"jest","postinstall":"node steal.js"/' package.json
echo 'export A=1' > .envrc
printf '#!/bin/sh\n' > run.sh && chmod +x run.sh
mkdir -p .github/workflows && echo 'on: push' > .github/workflows/ci.yml`)
	os.WriteFile(filepath.Join(p.Dir, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755)

	out, _, _ := p.run(ctx, nil, "audit")
	for _, want := range []string{"added", ".git/hooks/pre-commit", "script-added", "postinstall", ".envrc", "run.sh", ".github/workflows/ci.yml"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit missing %q:\n%s", want, out)
		}
	}
	js, _, _ := p.run(ctx, nil, "audit", "--json")
	if !strings.Contains(js, `"kind": "script-added"`) {
		t.Errorf("json output:\n%s", js)
	}
	// Acknowledge: the report empties and stays empty.
	if _, se, code := p.run(ctx, nil, "audit", "--accept"); code != 0 {
		t.Fatalf("accept: %s", se)
	}
	if out, _, _ := p.run(ctx, nil, "audit"); !strings.Contains(out, "no changes") {
		t.Errorf("after accept:\n%s", out)
	}
}

// Detach must summarize what the session changed.
func TestAuditSummaryOnShellExit(t *testing.T) {
	p := newProject(t, "audsum", "[sandbox]\nui = \"shell\"\n")
	s := p.attach()
	s.waitFor(t, "$", 5*time.Minute)
	s.pty.Write([]byte("echo 'x:' > Makefile; exit\r"))
	s.waitFor(t, "added", 30*time.Second)
	s.waitFor(t, "Makefile", 5*time.Second)
	s.closeTerminal()
}

// .airlockignore hides files inside the sandbox.
func TestAirlockIgnoreHidesFiles(t *testing.T) {
	p := newProject(t, "mask", "")
	os.WriteFile(filepath.Join(p.Dir, ".env"), []byte("TOKEN=hunter2"), 0o644)
	os.WriteFile(filepath.Join(p.Dir, "server.pem"), []byte("PRIVATE KEY"), 0o644)
	os.MkdirAll(filepath.Join(p.Dir, "secrets"), 0o755)
	os.WriteFile(filepath.Join(p.Dir, "secrets", "a.txt"), []byte("hidden"), 0o644)
	os.WriteFile(filepath.Join(p.Dir, "app.txt"), []byte("visible"), 0o644)
	os.WriteFile(filepath.Join(p.Dir, ".airlockignore"), []byte(".env*\n*.pem\nsecrets/\n"), 0o644)

	out := p.mustSh(`
echo "env-size=$(stat -c %s .env)"
echo "pem-size=$(stat -c %s server.pem)"
echo "secrets-listing=[$(ls secrets)]"
echo "app=$(cat app.txt)"
(echo overwrite > .env) 2>/dev/null && echo ENV-WRITABLE || echo env-readonly
grep -r hunter2 . 2>/dev/null | head -1
`)
	for _, want := range []string{"env-size=0", "pem-size=0", "secrets-listing=[]", "app=visible", "env-readonly"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "ENV-WRITABLE") {
		t.Errorf("secret visible or mask writable:\n%s", out)
	}
	// The host's files are untouched.
	if b, _ := os.ReadFile(filepath.Join(p.Dir, ".env")); string(b) != "TOKEN=hunter2" {
		t.Errorf("host .env changed: %q", b)
	}
}

// Git push mode: only the token and a scoped credential helper, never ssh keys.
func TestGitModes(t *testing.T) {
	local := newProject(t, "gitlocal", "")
	out := local.mustSh(`
git config --global --get-regexp '^credential' || echo no-credential-config
echo "token=${AIRLOCK_GIT_TOKEN:-unset}"
ls ~/.ssh 2>&1 | head -1
git config --global user.name; git config --global user.email
`)
	for _, want := range []string{"no-credential-config", "token=unset", "Test User", "test@example.org"} {
		if !strings.Contains(out, want) {
			t.Errorf("local mode, missing %q:\n%s", want, out)
		}
	}

	push := newProject(t, "gitpush", "[git]\nmode = \"push\"\ntoken_env = \"AIRLOCK_IT_TOKEN\"\nhosts = [\"git.example.org\"]\n")
	push.extra = []string{"AIRLOCK_IT_TOKEN=tok-123"}
	out = push.mustSh(`
printf 'protocol=https\nhost=git.example.org\n\n' | git credential fill
echo ---
printf 'protocol=https\nhost=other.example.org\n\n' | GIT_TERMINAL_PROMPT=0 git credential fill 2>&1 | head -2
ls ~/.ssh 2>&1 | head -1
`)
	if !strings.Contains(out, "password=tok-123") || !strings.Contains(out, "username=x-access-token") {
		t.Errorf("token not offered to the configured host:\n%s", out)
	}
	after := out[strings.Index(out, "---"):]
	if strings.Contains(after, "tok-123") {
		t.Errorf("token offered to a host that is not configured:\n%s", after)
	}
}

// doctor runs against a live sandbox and finds the firewall in place.
func TestDoctor(t *testing.T) {
	p := newProject(t, "doc", "")
	os.WriteFile(filepath.Join(p.Dir, ".env"), []byte("x"), 0o644)
	p.mustSh("true")
	ctx, cancel := ctxMinutes(2)
	defer cancel()
	out, se, code := p.run(ctx, nil, "doctor")
	if code != 0 {
		t.Fatalf("doctor failed (%d):\n%s\n%s", code, out, se)
	}
	for _, want := range []string{"container engine", "firewall", "egress rules installed", "sessions run without NET_ADMIN", "no Docker socket", "secrets", ".env"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
	js, _, _ := p.run(ctx, nil, "doctor", "--json")
	if !strings.Contains(js, `"status": "ok"`) {
		t.Errorf("doctor --json:\n%s", js)
	}
}
