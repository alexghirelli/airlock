package cli

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/sandbox"
)

// fakeHerdr is an inboxBackend: it lists the sandboxes it is given and answers
// `herdr agent list` with a scripted reply per sandbox.
type fakeHerdr struct {
	list    []backend.Info
	replies map[string]fakeReply

	mu    sync.Mutex
	asked map[string][]string // sandbox -> the command run there
}

type fakeReply struct {
	code   int
	stdout string
	stderr string
	err    error
}

func (f *fakeHerdr) List(context.Context) ([]backend.Info, error) { return f.list, nil }

func (f *fakeHerdr) Exec(_ context.Context, name string, opts backend.ExecOpts) (int, error) {
	f.mu.Lock()
	if f.asked == nil {
		f.asked = map[string][]string{}
	}
	f.asked[name] = opts.Cmd
	f.mu.Unlock()
	r := f.replies[name]
	if opts.Stdout != nil && r.stdout != "" {
		if _, err := io.WriteString(opts.Stdout, r.stdout); err != nil {
			return 0, err
		}
	}
	if opts.Stderr != nil && r.stderr != "" {
		if _, err := io.WriteString(opts.Stderr, r.stderr); err != nil {
			return 0, err
		}
	}
	return r.code, r.err
}

func sandboxInfo(name string, running bool, labels map[string]string) backend.Info {
	return backend.Info{Name: name, Running: running, Labels: labels}
}

func managed(project string) map[string]string {
	return map[string]string{backend.LabelManaged: "true", backend.LabelProject: project}
}

// herdrSample is the output of `herdr agent list` from Herdr 0.9.5.
const herdrSample = `{"id":"cli:agent:list","result":{"agents":[{"agent":"pi","agent_session":{"agent":"pi","kind":"path","source":"herdr:pi","value":"/creds/pi-agent/sessions/--tmp-app--/2026-11-11T16-21-26-886Z_01a1.jsonl"},"agent_status":"idle","cwd":"/tmp/tmp.VcCRbW9x3T/app","focused":true,"foreground_cwd":"/tmp/tmp.VcCRbW9x3T/app","interactive_ready":true,"name":"pi","pane_id":"w1:p1","revision":1,"screen_detection_skipped":true,"state_change_seq":1,"tab_id":"w1:t1","terminal_id":"term_66002963762d01","terminal_title":"π - app","terminal_title_stripped":"π - app","workspace_id":"w1"}],"type":"agent_list"}}`

const noServer = `{"id":"cli:agent:list","error":{"code":"server_not_running","message":"the Herdr server is not running"}}`

func TestHerdrCommandRunsUnderTimeoutAsTheSessionUser(t *testing.T) {
	want := []string{sandbox.SessionWrapper, "timeout", "4", "herdr", "agent", "list"}
	if !reflect.DeepEqual(herdrListCmd, want) {
		t.Errorf("herdrListCmd = %q, want %q", herdrListCmd, want)
	}
}

func TestInboxSnapshotOutcomes(t *testing.T) {
	big := strings.Repeat("x", herdrReplyLimit+1)
	f := &fakeHerdr{
		list: []backend.Info{
			sandboxInfo("airlock-app", true, managed("/home/u/app")),
			sandboxInfo("airlock-none", true, managed("/home/u/none")),
			sandboxInfo("airlock-stderr", true, managed("/home/u/stderr")),
			sandboxInfo("airlock-exit", true, managed("/home/u/exit")),
			sandboxInfo("airlock-err", true, managed("/home/u/err")),
			sandboxInfo("airlock-big", true, managed("/home/u/big")),
			sandboxInfo("airlock-junk", true, managed("/home/u/junk")),
			sandboxInfo("airlock-stopped", false, managed("/home/u/stopped")),
			sandboxInfo("other-container", true, map[string]string{backend.LabelProject: "/home/u/x"}),
			sandboxInfo("airlock-noproject", true, map[string]string{backend.LabelManaged: "true"}),
		},
		replies: map[string]fakeReply{
			"airlock-app":    {code: 0, stdout: herdrSample},
			"airlock-none":   {code: 1, stdout: noServer},
			"airlock-stderr": {code: 1, stderr: noServer},
			"airlock-exit":   {code: 3, stdout: "something"},
			"airlock-err":    {err: errors.New("exec: connection reset")},
			"airlock-big":    {code: 0, stdout: big},
			"airlock-junk":   {code: 0, stdout: "not json"},
		},
	}
	agents, failed, err := inboxSnapshot(context.Background(), f, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Sandbox != "airlock-app" || agents[0].Name != "pi" {
		t.Errorf("agents = %+v, want the one agent of airlock-app", agents)
	}
	wantFailed := map[string]bool{"airlock-exit": true, "airlock-err": true, "airlock-big": true, "airlock-junk": true}
	if !reflect.DeepEqual(failed, wantFailed) {
		t.Errorf("failed = %v, want %v", failed, wantFailed)
	}
	// Only running, airlock-managed sandboxes with a project are asked, and each with the same command.
	asked := map[string]bool{}
	for name, cmd := range f.asked {
		asked[name] = true
		if !reflect.DeepEqual(cmd, herdrListCmd) {
			t.Errorf("%s was asked with %q", name, cmd)
		}
	}
	for _, name := range []string{"airlock-stopped", "other-container", "airlock-noproject"} {
		if asked[name] {
			t.Errorf("%s was queried but must not be", name)
		}
	}
	if len(asked) != 7 {
		t.Errorf("queried %d sandboxes, want 7: %v", len(asked), asked)
	}
}

func TestInboxSnapshotNoSandboxes(t *testing.T) {
	agents, failed, err := inboxSnapshot(context.Background(), &fakeHerdr{}, ".worktrees")
	if err != nil || len(agents) != 0 || len(failed) != 0 {
		t.Errorf("agents %v, failed %v, err %v", agents, failed, err)
	}
}
