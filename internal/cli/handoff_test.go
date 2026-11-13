package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/inbox"
)

func TestParseArgsHandoff(t *testing.T) {
	a, err := parseArgs([]string{"wt", "handoff", "rate-limit", "--agent", "claude", "--force", "the", "tests"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.rest, []string{"handoff", "rate-limit", "the", "tests"}) {
		t.Errorf("rest = %q", a.rest)
	}
	if a.agent != "claude" || !a.force || a.reset {
		t.Errorf("--agent claude --force: agent %q, force %v, reset %v", a.agent, a.force, a.reset)
	}

	a, err = parseArgs([]string{"wt", "handoff", "rate-limit", "--reset"})
	if err != nil || !a.reset || a.force || a.agent != "" {
		t.Errorf("--reset: %+v, %v", a, err)
	}

	if _, err := parseArgs([]string{"wt", "handoff", "rate-limit", "--agent"}); err == nil {
		t.Error("--agent without a value was accepted")
	}
}

// The handoff reads the agents of its sandbox with the same call as the inbox.
func TestHerdrAgentsRaw(t *testing.T) {
	f := &fakeHerdr{replies: map[string]fakeReply{
		"airlock-app":  {code: 0, stdout: herdrSample},
		"airlock-none": {code: 1, stdout: noServer},
		"airlock-junk": {code: 0, stdout: "not json"},
		"airlock-err":  {err: errors.New("exec: connection reset")},
	}}
	raw, err := herdrAgents(context.Background(), f, "airlock-app")
	if err != nil || len(raw) != 1 || raw[0].Agent != "pi" || raw[0].AgentSession == nil ||
		raw[0].AgentSession.Value != "/creds/pi-agent/sessions/--tmp-app--/2026-11-11T16-21-26-886Z_01a1.jsonl" ||
		raw[0].Cwd != "/tmp/tmp.VcCRbW9x3T/app" {
		t.Errorf("answer: %+v, %v", raw, err)
	}
	if raw, err := herdrAgents(context.Background(), f, "airlock-none"); err != nil || raw != nil {
		t.Errorf("no server: %+v, %v; want no agents and no error", raw, err)
	}
	if _, err := herdrAgents(context.Background(), f, "airlock-junk"); !errors.Is(err, errBadReply) {
		t.Errorf("bad reply: %v, want errBadReply", err)
	}
	if _, err := herdrAgents(context.Background(), f, "airlock-err"); err == nil {
		t.Error("a failed exec was not reported")
	}
	if got := f.asked["airlock-app"]; !reflect.DeepEqual(got, herdrListCmd) {
		t.Errorf("asked with %q", got)
	}
}

// The worktree of herdrSample: its agent (pi, idle) runs in this folder.
const handoffWorktree = "/tmp/tmp.VcCRbW9x3T/app"

// handoffFixture is the handoff of the worktree, with the fake Herdr of the
// sandbox "airlock-app". Every step appends its name to steps; the briefing
// written is kept in briefing. When goneAfterClose is set, closing the
// workspace removes the agent from the sandbox, as Herdr does.
type handoffFixture struct {
	f        *fakeHerdr
	steps    []string
	briefing string
	run      handoffRun
}

func newHandoffFixture(replies fakeReply, force, goneAfterClose bool) *handoffFixture {
	fx := &handoffFixture{f: &fakeHerdr{replies: map[string]fakeReply{"airlock-app": replies}}}
	fx.run = handoffRun{
		name: "app", path: handoffWorktree, note: "the retry path fails", kind: "claude", force: force,
		agents: func(ctx context.Context) ([]inbox.Raw, error) {
			fx.steps = append(fx.steps, "agents")
			return herdrAgents(ctx, fx.f, "airlock-app")
		},
		brief: func() (string, error) {
			fx.steps = append(fx.steps, "brief")
			return "# Changes in app\n", nil
		},
		write: func(text string) error {
			fx.steps = append(fx.steps, "write")
			fx.briefing = text
			return nil
		},
		closeWS: func(context.Context) error {
			fx.steps = append(fx.steps, "close")
			if goneAfterClose {
				fx.f.replies["airlock-app"] = fakeReply{code: 1, stdout: noServer}
			}
			return nil
		},
		start: func(context.Context) (int, error) {
			fx.steps = append(fx.steps, "start")
			return 0, nil
		},
		log:   func(string, ...any) {},
		tries: 3,
	}
	return fx
}

var (
	idleHerdr    = fakeReply{code: 0, stdout: herdrSample}
	workingHerdr = fakeReply{code: 0, stdout: strings.Replace(herdrSample, `"agent_status":"idle"`, `"agent_status":"working"`, 1)}
	noAgents     = fakeReply{code: 1, stdout: noServer}
)

func TestHandoffRefusesWhileWorking(t *testing.T) {
	fx := newHandoffFixture(workingHerdr, false, true)
	_, err := fx.run.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still working") {
		t.Fatalf("working previous agent: err = %v", err)
	}
	if want := []string{"agents"}; !reflect.DeepEqual(fx.steps, want) {
		t.Errorf("steps = %q, want %q: nothing may be written or closed", fx.steps, want)
	}
}

func TestHandoffForceOverridesWorking(t *testing.T) {
	fx := newHandoffFixture(workingHerdr, true, true)
	if _, err := fx.run.run(context.Background()); err != nil {
		t.Fatalf("--force: %v", err)
	}
	want := []string{"agents", "brief", "write", "close", "agents", "start"}
	if !reflect.DeepEqual(fx.steps, want) {
		t.Errorf("steps = %q, want %q", fx.steps, want)
	}
	if !strings.Contains(fx.briefing, "from a previous agent (`pi`)") {
		t.Errorf("briefing does not name the previous agent:\n%s", fx.briefing)
	}
}

// The briefing is written before the workspace is closed, and the new agent
// starts only after the close is confirmed.
func TestHandoffOrderAndConfirmedClose(t *testing.T) {
	fx := newHandoffFixture(idleHerdr, false, true)
	if _, err := fx.run.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"agents", "brief", "write", "close", "agents", "start"}
	if !reflect.DeepEqual(fx.steps, want) {
		t.Errorf("steps = %q, want %q", fx.steps, want)
	}
	for _, line := range []string{"from a previous agent (`pi`)", "## Note from the user", "the retry path fails", "# Changes in app"} {
		if !strings.Contains(fx.briefing, line) {
			t.Errorf("briefing lacks %q:\n%s", line, fx.briefing)
		}
	}
}

func TestHandoffWithoutPreviousAgent(t *testing.T) {
	fx := newHandoffFixture(noAgents, false, false)
	if _, err := fx.run.run(context.Background()); err != nil {
		t.Fatalf("no Herdr server: %v", err)
	}
	if !strings.Contains(fx.briefing, "no previous agent was running") {
		t.Errorf("briefing does not say there was none:\n%s", fx.briefing)
	}
	if fx.steps[len(fx.steps)-1] != "start" {
		t.Errorf("steps = %q: the new agent did not start", fx.steps)
	}
}

// When the agent is still listed after the close, nothing is started; the
// briefing is already on disk for the next try.
func TestHandoffStopsWhenPreviousAgentSurvivesTheClose(t *testing.T) {
	fx := newHandoffFixture(idleHerdr, false, false)
	_, err := fx.run.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still running in app") || !strings.Contains(err.Error(), "close it in Herdr and rerun") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range fx.steps {
		if s == "start" {
			t.Errorf("the new agent was started over a running one: %q", fx.steps)
		}
	}
	want := []string{"agents", "brief", "write", "close", "agents", "agents", "agents"}
	if !reflect.DeepEqual(fx.steps, want) {
		t.Errorf("steps = %q, want %q", fx.steps, want)
	}
	if fx.briefing == "" {
		t.Error("the briefing was not written before the close")
	}
}

func TestHandoffStopsWhenTheCloseCannotBeConfirmed(t *testing.T) {
	fx := newHandoffFixture(idleHerdr, false, false)
	fx.run.closeWS = func(context.Context) error { return errors.New("docker exec failed") }
	if _, err := fx.run.run(context.Background()); err == nil {
		t.Fatal("a failed close went on")
	}
	for _, s := range fx.steps {
		if s == "start" {
			t.Errorf("started after a failed close: %q", fx.steps)
		}
	}
}

// A list that cannot be read: refused without --force, handed over with it.
func TestHandoffUnreadableListNeedsForce(t *testing.T) {
	broken := fakeReply{err: errors.New("exec: connection reset")}
	fx := newHandoffFixture(broken, false, true)
	if _, err := fx.run.run(context.Background()); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("without --force: %v", err)
	}
	if want := []string{"agents"}; !reflect.DeepEqual(fx.steps, want) {
		t.Errorf("steps = %q, want %q", fx.steps, want)
	}

	fx = newHandoffFixture(broken, true, true)
	if _, err := fx.run.run(context.Background()); err != nil {
		t.Fatalf("with --force: %v", err)
	}
	if fx.steps[len(fx.steps)-1] != "start" {
		t.Errorf("steps = %q: --force did not hand over", fx.steps)
	}
}
