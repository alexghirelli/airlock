//go:build integration

package tests

import (
	"strings"
	"testing"
)

// The sessions these tests fake go into a credentials volume of their own, which
// the test removes (rm --purge), not into the one every other test shares.
const perProjectLogins = "[credentials]\nscope = \"per-project\"\n\n"

func purgeAtEnd(t *testing.T, p *project) {
	t.Cleanup(func() {
		ctx, cancel := ctxMinutes(2)
		defer cancel()
		_, _, _ = p.run(ctx, nil, "rm", "--purge")
	})
}

// Reopening a workspace (a closed worktree, a restarted sandbox) continues the
// conversation the agent had in that directory. The sessions live in the credentials
// volume, so they outlive the container; herdr-up adds --continue when there is one.
func TestAgentContinuesItsLastConversation(t *testing.T) {
	p := newProject(t, "resume", perProjectLogins)
	purgeAtEnd(t, p)
	// The arguments herdr-up would give Pi, without starting Herdr (a test hook).
	args := func(flags string) string {
		return p.mustSh(`AIRLOCK_RESUME_ARGS_ONLY=1 airlock-herdr-up --agent pi ` + flags)
	}
	// Pi writes its session file with the first message, and names the folder after the path.
	seed := func(dir string) {
		p.mustSh(`d=` + dir + `; enc="--${d#/}--"; enc="${enc//\//-}"
mkdir -p "$PI_CODING_AGENT_DIR/sessions/$enc" && echo '{}' > "$PI_CODING_AGENT_DIR/sessions/$enc/2026-11-12T10-00-00Z_test.jsonl"`)
	}
	wt := `"$PWD/.worktrees/rate-limit"`

	if got := args(""); got != "" {
		t.Errorf("no conversation yet, yet herdr-up passes %q", got)
	}
	seed(`"$PWD"`)
	if got := args(""); got != "--\n--continue" {
		t.Errorf("a conversation exists, herdr-up passes %q, want -- --continue", got)
	}
	if got := args("--fresh"); got != "" {
		t.Errorf("--fresh still passes %q", got)
	}
	// A worktree has its own conversation: the project's does not leak into it.
	if got := args("--workspace " + wt); got != "" {
		t.Errorf("worktree without a conversation passes %q", got)
	}
	seed(wt)
	if got := args("--workspace " + wt); got != "--\n--continue" {
		t.Errorf("worktree with a conversation passes %q", got)
	}
	if got := p.mustSh(`AIRLOCK_RESUME_ARGS_ONLY=1 airlock-herdr-up --agent pi --team`); got != "" {
		t.Errorf("a team starts fresh, yet herdr-up passes %q", got)
	}
	// The session survives the sandbox: it is in the credentials volume.
	ctx, cancel := ctxMinutes(3)
	defer cancel()
	if _, errOut, code := p.run(ctx, nil, "rm"); code != 0 {
		t.Fatalf("rm: %d\n%s", code, errOut)
	}
	if out, _, code := p.run(ctx, nil, "exec", "--", "true"); code != 0 {
		t.Fatalf("exec after rm: %d\n%s", code, out)
	}
	if got := args(""); got != "--\n--continue" {
		t.Errorf("after rm and a new sandbox, herdr-up passes %q", got)
	}
}

// The arguments reach the agent itself: Herdr types the command into the pane, so a
// stand-in for Pi that records its command line shows what Herdr started.
func TestHerdrStartsTheAgentWithContinue(t *testing.T) {
	p := newProject(t, "resume-herdr", perProjectLogins)
	purgeAtEnd(t, p)
	p.mustSh(`printf '#!/bin/sh\necho "pi $*" >> /tmp/pi-started\nexec /usr/local/bin/pi "$@"\n' > ~/.local/bin/pi && chmod +x ~/.local/bin/pi
d="$PWD"; enc="--${d#/}--"; enc="${enc//\//-}"
mkdir -p "$PI_CODING_AGENT_DIR/sessions/$enc" && echo '{}' > "$PI_CODING_AGENT_DIR/sessions/$enc/2026-11-12T10-00-00Z_test.jsonl"`)
	// Whether Pi can load the made-up session does not matter here, only how it was started.
	_, _ = p.sh(`airlock-herdr-up --agent pi`)
	if got := p.mustSh(`cat /tmp/pi-started`); !strings.Contains(got, "--continue") {
		t.Errorf("Herdr started Pi without --continue:\n%s", got)
	}
}

func TestResumeCanBeTurnedOff(t *testing.T) {
	p := newProject(t, "resume-off", perProjectLogins+"[agent]\nresume = false\n")
	purgeAtEnd(t, p)
	p.mustSh(`d="$PWD"; enc="--${d#/}--"; enc="${enc//\//-}"
mkdir -p "$PI_CODING_AGENT_DIR/sessions/$enc" && echo '{}' > "$PI_CODING_AGENT_DIR/sessions/$enc/2026-11-12T10-00-00Z_test.jsonl"`)
	if got := p.mustSh(`AIRLOCK_RESUME_ARGS_ONLY=1 airlock-herdr-up --agent pi`); strings.TrimSpace(got) != "" {
		t.Errorf("agent.resume = false, yet herdr-up passes %q", got)
	}
}
