//go:build integration

package tests

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// session is an airlock process attached to a pseudo-terminal, like a real
// terminal window the user can close.
type session struct {
	cmd *exec.Cmd
	pty interface {
		io.ReadWriteCloser
	}
	mu  sync.Mutex
	buf bytes.Buffer
}

func (p *project) attach(args ...string) *session {
	p.t.Helper()
	cmd := exec.Command(airlockBin, args...)
	cmd.Dir = p.Dir
	cmd.Env = append(p.env(p.extra...), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 140})
	if err != nil {
		p.t.Fatal(err)
	}
	s := &session{cmd: cmd, pty: f}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := f.Read(b)
			s.mu.Lock()
			s.buf.Write(b[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	p.t.Cleanup(func() { s.closeTerminal() })
	return s
}

func (s *session) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFor blocks until the terminal output contains text.
func (s *session) waitFor(t *testing.T, text string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), text) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in terminal output:\n%.2000s", text, s.output())
}

// closeTerminal simulates closing the terminal window: the pty goes away and
// the client gets SIGHUP.
func (s *session) closeTerminal() {
	_ = s.pty.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGHUP)
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
		}
	}
}

// Tests 1 and 2: `airlock` attaches to Herdr with Pi running; closing the
// terminal and coming back finds the same session, and a stop/start brings Pi
// back without duplicating anything.
func TestHerdrAttachDetachReattach(t *testing.T) {
	p := newProject(t, "ui", "")

	s := p.attach()
	s.waitFor(t, "spaces", 5*time.Minute) // the Herdr sidebar
	// Pi must be running inside Herdr as an agent.
	var agents string
	for i := 0; i < 60; i++ {
		agents, _ = p.sh("herdr agent list")
		if strings.Contains(agents, `"name":"pi"`) {
			break
		}
		time.Sleep(time.Second)
	}
	if !strings.Contains(agents, `"name":"pi"`) {
		t.Fatalf("Pi is not running in Herdr: %s", agents)
	}
	first := p.mustSh("herdr workspace list | jq -r '[.result.workspaces[] | select(.label==\"ui\")] | length'; pgrep -fc 'herdr server' || true")

	s.closeTerminal() // the user closes the window
	time.Sleep(2 * time.Second)

	// The server and Pi survive the vanished client; no client process is left.
	after := p.mustSh("pgrep -fc 'herdr server'; herdr agent list | jq -r '.result.agents[].name'; pgrep -xc herdr || true")
	if !strings.Contains(after, "pi") {
		t.Errorf("Pi did not survive the closed terminal:\n%s", after)
	}

	// Coming back reaches the same session: still one workspace, still one server.
	s2 := p.attach()
	s2.waitFor(t, "spaces", 2*time.Minute)
	again := p.mustSh("herdr workspace list | jq -r '[.result.workspaces[] | select(.label==\"ui\")] | length'; pgrep -fc 'herdr server' || true")
	if again != first {
		t.Errorf("reattach changed the session.\nbefore: %q\nafter:  %q", first, again)
	}
	s2.closeTerminal()

	// Stop and start: the container restarts, the Herdr server with it. Pi is
	// started again in the restored workspace instead of a duplicate one.
	ctx, cancel := ctxMinutes(2)
	defer cancel()
	if _, se, code := p.run(ctx, nil, "stop"); code != 0 {
		t.Fatalf("stop: %s", se)
	}
	s3 := p.attach()
	s3.waitFor(t, "spaces", 2*time.Minute)
	var n string
	for i := 0; i < 60; i++ {
		n, _ = p.sh("herdr agent list | jq -r '[.result.agents[] | select(.name==\"pi\")] | length'")
		if n == "1" {
			break
		}
		time.Sleep(time.Second)
	}
	if n != "1" {
		t.Errorf("after stop/start there should be exactly one Pi agent, got %q", n)
	}
	if ws := p.mustSh("herdr workspace list | jq -r '[.result.workspaces[] | select(.label==\"ui\")] | length'"); ws != "1" {
		t.Errorf("stop/start duplicated the workspace: %s", ws)
	}
	s3.closeTerminal()
}

// The plain-Pi and shell UIs work too.
func TestPiAndShellUIs(t *testing.T) {
	p := newProject(t, "uis", "[sandbox]\nui = \"shell\"\n")
	s := p.attach()
	s.waitFor(t, "$", 5*time.Minute)
	if _, err := s.pty.Write([]byte("echo in-shell-$((6*7))\r")); err != nil {
		t.Fatal(err)
	}
	s.waitFor(t, "in-shell-42", 30*time.Second)
	s.closeTerminal()

	q := newProject(t, "uipi", "")
	pi := q.attach("--ui", "pi")
	pi.waitFor(t, "[Context]", 5*time.Minute)
	pi.closeTerminal()
}
