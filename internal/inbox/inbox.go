// Package inbox holds the logic of `airlock inbox`: what the agents of the
// running sandboxes are doing, which of them need the user, and how that is
// shown. It does no I/O, so the rules can be tested on their own.
//
// Everything an agent reports (its name, its directory, its status, its title)
// is written by code inside the sandbox, so it is untrusted. It goes through
// Clean before it reaches a terminal or a notification, and it is only ever
// passed to programs as argv, never through a shell.
package inbox

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"
)

// Statuses an agent can report. Anything else is shown as unknown.
const (
	Blocked = "blocked" // waiting for the user: a question, a permission
	Done    = "done"    // finished its turn
	Idle    = "idle"    // waiting for a prompt
	Working = "working"
	Unknown = "unknown"
)

// statusRank is the order of the list: what needs you first.
var statusRank = map[string]int{Blocked: 0, Done: 1, Idle: 2, Working: 3, Unknown: 4}

// MaxField is the longest text shown for a field of an agent.
const MaxField = 80

// MaxAgents is how many agents of one sandbox are taken into account. A sandbox
// that reports more is cut short, so it cannot flood the list or the notifications.
const MaxAgents = 50

// Agent is one agent of one sandbox, as airlock shows it. The text fields other
// than Sandbox are cleaned (see Clean), and Status is one of the known statuses.
type Agent struct {
	// Sandbox is the container name. It is host-side and not cleaned, so it
	// stays exact and matches the sandbox in Carry.
	Sandbox  string `json:"sandbox"`
	Project  string `json:"project"`
	Worktree string `json:"worktree"`
	Name     string `json:"agent"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`

	// ident tells agents apart: the name, or the pane when the name is empty.
	ident string
}

// id is the identity of the agent within its sandbox.
func (a Agent) id() string {
	if a.ident != "" {
		return a.ident
	}
	return a.Name
}

// Raw is one entry of `herdr agent list`.
type Raw struct {
	Agent       string `json:"agent"`
	AgentStatus string `json:"agent_status"`
	Cwd         string `json:"cwd"`
	Name        string `json:"name"`
	PaneID      string `json:"pane_id"`
}

// Answer is what a sandbox's `herdr agent list` said.
type Answer int

const (
	Answered Answer = iota // a list of agents, possibly empty
	NoServer               // no Herdr server runs there: no agents, and not a failure
	Failed                 // anything else: the sandbox could not be read
)

// Classify reads the result of `herdr agent list`: the exit code and the reply
// (its stdout, or stderr when stdout is empty). Exit 0 with a result is a list.
// Exit 1 with error.code "server_not_running" is a sandbox without Herdr. Any
// other outcome, including output that is not JSON, is a failure.
func Classify(code int, reply []byte) (Answer, []Raw) {
	var doc struct {
		Result *struct {
			Agents []Raw `json:"agents"`
		} `json:"result"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	err := json.Unmarshal(reply, &doc)
	switch {
	case code == 0 && err == nil && doc.Result != nil:
		return Answered, doc.Result.Agents
	case code == 1 && err == nil && doc.Error != nil && doc.Error.Code == "server_not_running":
		return NoServer, nil
	}
	return Failed, nil
}

// Build turns the agents of one sandbox into rows: the worktree each runs in,
// the status mapped to the known ones, and the text cleaned. At most MaxAgents
// are taken. project is the project's root on the host (the sandbox's
// io.airlock.project label); worktreesDir is sandbox.worktrees_dir from the
// user's config.
func Build(sandbox, project, worktreesDir string, raw []Raw) []Agent {
	if len(raw) > MaxAgents {
		raw = raw[:MaxAgents]
	}
	out := make([]Agent, 0, len(raw))
	for i, r := range raw {
		ident := r.Name
		if ident == "" {
			ident = r.PaneID
		}
		if ident == "" {
			ident = "#" + strconv.Itoa(i)
		}
		out = append(out, Agent{
			Sandbox:  sandbox,
			Project:  Clean(path.Base(project), MaxField),
			Worktree: Clean(Worktree(project, worktreesDir, r.Cwd), MaxField),
			Name:     Clean(r.Name, MaxField),
			Kind:     Clean(r.Agent, MaxField),
			Status:   KnownStatus(r.AgentStatus),
			ident:    ident,
		})
	}
	return out
}

// Worktree names the worktree a directory is in: the first folder under the
// project's worktrees directory. It is "-" for anything else, including the
// project's own folder and isolated worktrees, which have a sandbox of their own.
func Worktree(project, worktreesDir, cwd string) string {
	dir := strings.Trim(path.Clean(strings.ReplaceAll(worktreesDir, `\`, "/")), "/")
	if dir == "" || dir == "." || project == "" || cwd == "" {
		return "-"
	}
	prefix := path.Join(path.Clean(project), dir) + "/"
	rest, ok := strings.CutPrefix(path.Clean(cwd), prefix)
	if !ok {
		return "-"
	}
	name, _, _ := strings.Cut(rest, "/")
	if name == "" || name == "." || name == ".." {
		return "-"
	}
	return name
}

// KnownStatus returns s when it is a status airlock knows, else unknown.
func KnownStatus(s string) string {
	if _, ok := statusRank[s]; ok {
		return s
	}
	return Unknown
}

// Clean makes text from a sandbox safe to print. It removes control characters
// (below 0x20, 0x7f and the C1 range 0x80-0x9f), terminal escape sequences as a
// whole, Unicode format characters (category Cf: zero-width characters, bidi
// controls, byte order marks) and the line and paragraph separators, then cuts
// the result to limit characters. It does not make text safe for every
// consumer: it removes what breaks a terminal's layout or hides text.
func Clean(s string, limit int) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b:
			i = skipEscape(rs, i)
			continue
		case r == 0x9b: // the one-character form of CSI
			i = skipUntilFinal(rs, i+1)
			continue
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			continue
		case r == 0x2028, r == 0x2029, unicode.Is(unicode.Cf, r):
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if limit > 0 && utf8.RuneCountInString(out) > limit {
		if limit <= 3 {
			return string([]rune(out)[:limit])
		}
		return strings.TrimSpace(string([]rune(out)[:limit-3])) + "..."
	}
	return out
}

// skipEscape returns the index of the last rune of the escape sequence that
// starts at rs[i] (an ESC).
func skipEscape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i
	}
	switch next := rs[i+1]; {
	case next == '[':
		return skipUntilFinal(rs, i+2)
	case next == ']': // OSC: ends with BEL or ST (ESC \)
		for j := i + 2; j < len(rs); j++ {
			if rs[j] == 0x07 {
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
		return len(rs) - 1
	case next >= 0x20 && next <= 0x2f: // intermediate bytes, then a final byte
		j := i + 2
		for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
			j++
		}
		if j < len(rs) {
			return j
		}
		return len(rs) - 1
	}
	return i + 1 // a two-character escape such as ESC c
}

// skipUntilFinal returns the index of the final byte (0x40-0x7e) of a control
// sequence whose parameters start at rs[i], or the last index if there is none.
func skipUntilFinal(rs []rune, i int) int {
	for j := i; j < len(rs); j++ {
		if rs[j] >= 0x40 && rs[j] <= 0x7e {
			return j
		}
	}
	return len(rs) - 1
}

// Sort orders the rows: what needs you first (blocked, done, idle, working,
// unknown), then by project, agent name and sandbox.
func Sort(agents []Agent) {
	sort.SliceStable(agents, func(i, j int) bool {
		a, b := agents[i], agents[j]
		if ra, rb := statusRank[a.Status], statusRank[b.Status]; ra != rb {
			return ra < rb
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Sandbox < b.Sandbox
	})
}

// WriteTable prints one row per agent with the columns PROJECT, WORKTREE,
// AGENT, KIND, STATUS. Empty fields show as "-".
func WriteTable(w io.Writer, agents []Agent) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tWORKTREE\tAGENT\tKIND\tSTATUS")
	for _, a := range agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", dash(a.Project), dash(a.Worktree), dash(a.Name), dash(a.Kind), dash(a.Status))
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Location is "project/worktree", or the project alone for its own folder.
func (a Agent) Location() string {
	if a.Worktree == "-" || a.Worktree == "" {
		return a.Project
	}
	return a.Project + "/" + a.Worktree
}

// EventKind is how an agent changed between two snapshots.
type EventKind int

const (
	Appeared EventKind = iota // the agent is new
	Changed                   // its status changed
	Gone                      // the agent is no longer listed
)

// Event is one change an agent went through between two polls.
type Event struct {
	Kind EventKind
	// Agent is the agent as it is now; for Gone, as it was.
	Agent Agent
	// From is the previous status of a Changed agent.
	From string
	// Notify is true when the user should be told: an agent is waiting for them,
	// or it finished or went idle after working.
	Notify bool
}

func key(a Agent) string { return a.Sandbox + "\x00" + a.id() }

// Diff compares two snapshots and returns what changed, sorted by sandbox and
// agent. A nil prev means nothing was seen before, so every agent appears.
func Diff(prev, next []Agent) []Event {
	before := make(map[string]Agent, len(prev))
	for _, a := range prev {
		before[key(a)] = a
	}
	after := make(map[string]Agent, len(next))
	for _, a := range next {
		after[key(a)] = a
	}
	keys := make([]string, 0, len(before)+len(after))
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var events []Event
	for _, k := range keys {
		old, hadOld := before[k]
		now, hasNow := after[k]
		switch {
		case !hadOld:
			events = append(events, Event{Kind: Appeared, Agent: now, Notify: now.Status == Blocked})
		case !hasNow:
			events = append(events, Event{Kind: Gone, Agent: old})
		case old.Status != now.Status:
			notify := now.Status == Blocked || (old.Status == Working && (now.Status == Done || now.Status == Idle))
			events = append(events, Event{Kind: Changed, Agent: now, From: old.Status, Notify: notify})
		}
	}
	return events
}

// Carry keeps the previous rows of the sandboxes whose query failed this time
// (a timeout, a dropped connection), so a hiccup does not look like every agent
// stopping and starting again.
func Carry(prev, next []Agent, failed map[string]bool) []Agent {
	if len(failed) == 0 {
		return next
	}
	out := append([]Agent(nil), next...)
	for _, a := range prev {
		if failed[a.Sandbox] {
			out = append(out, a)
		}
	}
	return out
}

// Describe is one line about an event, without a timestamp.
func Describe(e Event) string {
	a := e.Agent
	switch e.Kind {
	case Appeared:
		return fmt.Sprintf("%s: %s appeared, %s", a.Location(), a.Name, a.Status)
	case Changed:
		return fmt.Sprintf("%s: %s %s -> %s", a.Location(), a.Name, e.From, a.Status)
	}
	return fmt.Sprintf("%s: %s is gone", a.Location(), a.Name)
}

// Notice is the title and the message of one desktop notification for the
// notifying events of a single poll: one agent is named, several are counted.
func Notice(events []Event) (title, message string) {
	const t = "airlock"
	if len(events) == 1 {
		a := events[0].Agent
		name := a.Name
		if name == "" {
			name = a.Location()
		}
		return t, name + " " + verb(a.Status)
	}
	blocked := 0
	for _, e := range events {
		if e.Agent.Status == Blocked {
			blocked++
		}
	}
	if blocked == len(events) {
		return t, fmt.Sprintf("%d agents need you", len(events))
	}
	return t, fmt.Sprintf("%d agents need attention", len(events))
}

func verb(status string) string {
	switch status {
	case Blocked:
		return "needs you"
	case Done:
		return "finished"
	case Idle:
		return "is idle"
	}
	return "is " + status
}

// DesktopCommand is the argv that shows a notification, without the title and
// the message. A configured command wins. Otherwise: notify-send on Linux when
// it is installed, osascript on macOS, and nothing (bell only) elsewhere.
func DesktopCommand(configured []string, goos string, have func(program string) bool) []string {
	if len(configured) > 0 {
		return append([]string(nil), configured...)
	}
	switch goos {
	case "linux":
		if have("notify-send") {
			return []string{"notify-send"}
		}
	case "darwin":
		if have("osascript") {
			return []string{"osascript",
				"-e", "on run argv",
				"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
				"-e", "end run"}
		}
	}
	return nil
}

// Compose appends the title and the message to a notification command. For
// notify-send the text is rendered as markup by many notification daemons, so
// &, < and > are escaped, and "--" goes before the title so that a title
// starting with "-" is not read as an option. Other commands get plain arguments.
func Compose(prefix []string, title, message string) []string {
	if len(prefix) == 0 {
		return nil
	}
	argv := append([]string(nil), prefix...)
	if path.Base(prefix[0]) == "notify-send" {
		markup := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
		return append(argv, "--", markup.Replace(title), markup.Replace(message))
	}
	return append(argv, title, message)
}
