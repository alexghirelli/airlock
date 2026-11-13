package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/inbox"
	"github.com/alexghirelli/airlock/internal/sandbox"
)

const (
	// inboxInterval is how often --watch polls the sandboxes.
	inboxInterval = 5 * time.Second
	// inboxQueryTimeout bounds one sandbox's answer. The herdr call inside is
	// killed a little earlier (herdrListTimeout), so nothing is left running.
	inboxQueryTimeout = 5 * time.Second
	herdrListTimeout  = "4"
	// herdrReplyLimit bounds what one sandbox may send back; more is a failure.
	herdrReplyLimit = 1 << 20
	// notifyTimeout bounds a desktop or configured notification command.
	notifyTimeout = 5 * time.Second
)

// herdrListCmd runs `herdr agent list` as the session user, through the same
// wrapper as Herdr's own commands, under timeout so a hung herdr is killed.
var herdrListCmd = []string{sandbox.SessionWrapper, "timeout", herdrListTimeout, "herdr", "agent", "list"}

// errReplyTooLarge and errBadReply are the two ways a sandbox's answer can fail.
var (
	errReplyTooLarge = errors.New("the reply is larger than 1 MiB")
	errBadReply      = errors.New("the reply is not an agent list")
)

// inboxBackend is what the inbox needs from the backend; backend.Backend has it.
type inboxBackend interface {
	List(ctx context.Context) ([]backend.Info, error)
	Exec(ctx context.Context, name string, opts backend.ExecOpts) (int, error)
}

// cmdInbox lists the agents of the running sandboxes, or with --watch keeps
// polling and tells the user when one needs them.
func (e *env) cmdInbox(ctx context.Context) (int, error) {
	if e.a.watch && e.a.json {
		return 2, errors.New("--watch and --json cannot be used together")
	}
	res, err := e.loadUser()
	if err != nil {
		return 2, err
	}
	b, err := newBackend(&res.Config)
	if err != nil {
		return 1, err
	}
	if err := b.Ping(ctx); err != nil {
		return 1, err
	}
	wt := res.Config.Sandbox.WorktreesDir
	if e.a.watch {
		return e.watchInbox(ctx, b, wt, res.Config.Notify.Command)
	}
	agents, failed, err := inboxSnapshot(ctx, b, wt)
	if err != nil {
		return 1, err
	}
	for _, name := range sortedKeys(failed) {
		e.errf("airlock: warning: %s did not answer", name)
	}
	inbox.Sort(agents)
	if e.a.json {
		if agents == nil {
			agents = []inbox.Agent{}
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(agents)
	}
	e.printAgents(agents)
	return 0, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (e *env) printAgents(agents []inbox.Agent) {
	if len(agents) == 0 {
		fmt.Fprintln(e.stdout, "no agents in the running sandboxes")
		return
	}
	if err := inbox.WriteTable(e.stdout, agents); err != nil {
		e.errf("airlock: %v", err)
	}
}

// watchInbox polls until interrupted. The first poll prints the table; after
// that every change is printed. The ones that need the user are notified once
// per poll, as one notification.
func (e *env) watchInbox(ctx context.Context, b inboxBackend, worktreesDir string, cmd []string) (int, error) {
	e.errf("airlock: watching the sandboxes every %s; Ctrl-C stops", inboxInterval)
	var prev []inbox.Agent
	seen := false
	ticker := time.NewTicker(inboxInterval)
	defer ticker.Stop()
	for {
		now, failed, err := inboxSnapshot(ctx, b, worktreesDir)
		if ctx.Err() != nil {
			return 0, nil
		}
		if err != nil {
			e.errf("airlock: warning: %v", err)
		} else {
			now = inbox.Carry(prev, now, failed)
			inbox.Sort(now)
			events := inbox.Diff(prev, now)
			if !seen {
				seen = true
				e.printAgents(now)
			} else {
				stamp := time.Now().Format("15:04:05")
				for _, ev := range events {
					fmt.Fprintf(e.stdout, "%s  %s\n", stamp, inbox.Describe(ev))
				}
			}
			var notify []inbox.Event
			for _, ev := range events {
				if ev.Notify {
					notify = append(notify, ev)
				}
			}
			if len(notify) > 0 {
				e.notify(ctx, cmd, notify)
			}
			prev = now
		}
		select {
		case <-ctx.Done():
			return 0, nil
		case <-ticker.C:
		}
	}
}

// notify rings the bell and shows one desktop notification for the events of
// a poll, or runs the command from the user's config. The title and message
// are arguments, never shell text.
func (e *env) notify(ctx context.Context, configured []string, events []inbox.Event) {
	fmt.Fprint(e.stderr, "\a")
	base := inbox.DesktopCommand(configured, runtime.GOOS, haveProgram)
	if len(base) == 0 {
		return
	}
	title, message := inbox.Notice(events)
	argv := inbox.Compose(base, title, message)
	nctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	if err := exec.CommandContext(nctx, argv[0], argv[1:]...).Run(); err != nil {
		e.errf("airlock: warning: notification: %v", err)
	}
}

func haveProgram(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// inboxSnapshot asks every running airlock sandbox for its Herdr agents, at
// the same time. failed names the sandboxes that did not answer (a timeout, a
// failed exec, an unreadable reply); a sandbox without a Herdr server simply
// has no agents.
func inboxSnapshot(ctx context.Context, b inboxBackend, worktreesDir string) ([]inbox.Agent, map[string]bool, error) {
	list, err := b.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	type answer struct {
		rows []inbox.Agent
		err  error
	}
	var targets []backend.Info
	for _, i := range list {
		if i.Running && i.Labels[backend.LabelManaged] == "true" && i.Labels[backend.LabelProject] != "" {
			targets = append(targets, i)
		}
	}
	answers := make([]answer, len(targets))
	var wg sync.WaitGroup
	for n, t := range targets {
		wg.Add(1)
		go func(n int, t backend.Info) {
			defer wg.Done()
			rows, err := queryHerdr(ctx, b, t, worktreesDir)
			answers[n] = answer{rows: rows, err: err}
		}(n, t)
	}
	wg.Wait()

	var agents []inbox.Agent
	failed := map[string]bool{}
	for n, a := range answers {
		agents = append(agents, a.rows...)
		if a.err != nil {
			failed[targets[n].Name] = true
		}
	}
	return agents, failed, nil
}

// capBuffer keeps what is written to it, up to limit bytes. Writing past the
// limit fails and sets over, so a flood cannot grow the process's memory. It is
// locked because Exec returns on a timeout while its copy may still be writing.
type capBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		return 0, errReplyTooLarge
	}
	return c.buf.Write(p)
}

// result says whether the limit was hit and returns a copy of what was kept.
func (c *capBuffer) result() (over bool, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over, bytes.Clone(c.buf.Bytes())
}

// queryHerdr runs `herdr agent list` in one sandbox and classifies the answer
// (see inbox.Classify). It returns an error for anything that is not a list or
// a missing server, which the caller reports as a sandbox that did not answer.
func queryHerdr(ctx context.Context, b inboxBackend, sb backend.Info, worktreesDir string) ([]inbox.Agent, error) {
	raw, err := herdrAgents(ctx, b, sb.Name)
	if err != nil {
		return nil, err
	}
	return inbox.Build(sb.Name, sb.Labels[backend.LabelProject], worktreesDir, raw), nil
}

// herdrAgents is the raw `herdr agent list` of one sandbox, for the callers that
// need more than the inbox rows (the session path, the working directory). A
// sandbox without a Herdr server has no agents: nil, not an error.
func herdrAgents(ctx context.Context, b inboxBackend, name string) ([]inbox.Raw, error) {
	qctx, cancel := context.WithTimeout(ctx, inboxQueryTimeout)
	defer cancel()
	stdout := &capBuffer{limit: herdrReplyLimit}
	stderr := &capBuffer{limit: herdrReplyLimit}
	code, err := b.Exec(qctx, name, backend.ExecOpts{
		Cmd: herdrListCmd, User: "root", WorkDir: "/home/agent", Env: sandbox.SessionEnv(os.Getenv),
		Stdout: stdout, Stderr: stderr,
	})
	outOver, reply := stdout.result()
	errOver, errReply := stderr.result()
	if outOver || errOver {
		return nil, errReplyTooLarge
	}
	if err != nil {
		return nil, err
	}
	if len(reply) == 0 {
		reply = errReply
	}
	answer, raw := inbox.Classify(code, reply)
	switch answer {
	case inbox.NoServer:
		return nil, nil
	case inbox.Failed:
		return nil, errBadReply
	}
	return raw, nil
}
