package inbox

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// The output of Herdr 0.9.5, as `herdr agent list` prints it.
const herdrSample = `{"id":"cli:agent:list","result":{"agents":[{"agent":"pi","agent_session":{"agent":"pi","kind":"path","source":"herdr:pi","value":"/creds/pi-agent/sessions/--tmp-app--/2026-11-11T16-21-26-886Z_01a1.jsonl"},"agent_status":"idle","cwd":"/tmp/tmp.VcCRbW9x3T/app","focused":true,"foreground_cwd":"/tmp/tmp.VcCRbW9x3T/app","interactive_ready":true,"name":"pi","pane_id":"w1:p1","revision":1,"screen_detection_skipped":true,"state_change_seq":1,"tab_id":"w1:t1","terminal_id":"term_66002963762d01","terminal_title":"π - app","terminal_title_stripped":"π - app","workspace_id":"w1"}],"type":"agent_list"}}`

func TestClassify(t *testing.T) {
	noServer := `{"id":"cli:agent:list","error":{"code":"server_not_running","message":"the Herdr server is not running"}}`
	otherError := `{"id":"cli:agent:list","error":{"code":"internal","message":"boom"}}`
	cases := []struct {
		name   string
		code   int
		reply  string
		want   Answer
		agents int
	}{
		{"real herdr 0.9.5 list", 0, herdrSample, Answered, 1},
		{"empty list", 0, `{"result":{"agents":[],"type":"agent_list"}}`, Answered, 0},
		{"no server, the verified case", 1, noServer, NoServer, 0},
		{"no server with a different exit code", 2, noServer, Failed, 0},
		{"other error with exit 1", 1, otherError, Failed, 0},
		{"exit 0 but an error payload", 0, otherError, Failed, 0},
		{"exit 127 (no herdr binary)", 127, "", Failed, 0},
		{"timeout kills herdr (exit 124)", 124, "", Failed, 0},
		{"exit 0 with output that is not JSON", 0, "garbage", Failed, 0},
		{"exit 0 with no result", 0, `{"id":"x"}`, Failed, 0},
		{"no output at all", 1, "", Failed, 0},
	}
	for _, c := range cases {
		ans, raw := Classify(c.code, []byte(c.reply))
		if ans != c.want || len(raw) != c.agents {
			t.Errorf("%s: Classify = %v with %d agents, want %v with %d", c.name, ans, len(raw), c.want, c.agents)
		}
	}
	_, raw := Classify(0, []byte(herdrSample))
	if len(raw) != 1 || raw[0].Agent != "pi" || raw[0].AgentStatus != "idle" || raw[0].Cwd != "/tmp/tmp.VcCRbW9x3T/app" || raw[0].Name != "pi" || raw[0].PaneID != "w1:p1" {
		t.Errorf("parsed agent = %+v", raw)
	}
	// The transcript is kept too: the handoff briefing names it.
	if len(raw) == 1 && (raw[0].AgentSession == nil || raw[0].AgentSession.Value != "/creds/pi-agent/sessions/--tmp-app--/2026-11-11T16-21-26-886Z_01a1.jsonl") {
		t.Errorf("agent session = %+v", raw[0].AgentSession)
	}
}

func TestBuildCapsAgentsAndKeysEmptyNamesByPane(t *testing.T) {
	var raw []Raw
	for i := 0; i < MaxAgents+10; i++ {
		raw = append(raw, Raw{Agent: "pi", AgentStatus: "idle", Name: "pi-" + strconv.Itoa(i)})
	}
	if rows := Build("s", "/p", "", raw); len(rows) != MaxAgents {
		t.Errorf("rows = %d, want the cap %d", len(rows), MaxAgents)
	}
	// Two agents without a name are told apart by their panes.
	two := Build("s", "/p", "", []Raw{{AgentStatus: "blocked", PaneID: "w1:p1"}, {AgentStatus: "blocked", PaneID: "w1:p2"}})
	if ev := Diff(nil, two); len(ev) != 2 {
		t.Errorf("unnamed agents collapsed into %d event(s)", len(ev))
	}
	// And an agent that keeps its pane keeps its identity across polls.
	again := Build("s", "/p", "", []Raw{{AgentStatus: "done", PaneID: "w1:p1"}})
	if ev := Diff(two[:1], again); len(ev) != 1 || ev[0].Kind != Changed || ev[0].Agent.Status != Done {
		t.Errorf("same pane, changed status: %+v", ev)
	}
}

func TestBuildKeepsSandboxNameExact(t *testing.T) {
	odd := "airlock-x\x1b[0m"
	rows := Build(odd, "/p", "", []Raw{{Name: "pi", AgentStatus: "idle"}})
	if rows[0].Sandbox != odd {
		t.Errorf("sandbox name changed to %q", rows[0].Sandbox)
	}
}

func TestWorktree(t *testing.T) {
	cases := []struct {
		project, dir, cwd, want string
	}{
		{"/home/u/app", ".worktrees", "/home/u/app/.worktrees/review-pr-7", "review-pr-7"},
		{"/home/u/app", ".worktrees", "/home/u/app/.worktrees/review-pr-7/src", "review-pr-7"},
		{"/home/u/app", ".worktrees/", "/home/u/app/.worktrees/x", "x"},
		{"/home/u/app", ".worktrees", "/home/u/app", "-"},
		{"/home/u/app", ".worktrees", "/home/u/app/src", "-"},
		{"/home/u/app", ".worktrees", "/home/u/app/.worktrees", "-"},
		{"/home/u/app", ".worktrees", "/home/u/app/.worktreesX/y", "-"},
		{"/home/u/app", ".worktrees", "/home/u/app/.worktrees/../../etc", "-"},
		{"/home/u/app", "", "/home/u/app/.worktrees/x", "-"},
		{"/home/u/app", ".", "/home/u/app/x", "-"},
		{"/home/u/app", ".worktrees", "", "-"},
		{"", ".worktrees", "/x/.worktrees/y", "-"},
	}
	for _, c := range cases {
		if got := Worktree(c.project, c.dir, c.cwd); got != c.want {
			t.Errorf("Worktree(%q, %q, %q) = %q, want %q", c.project, c.dir, c.cwd, got, c.want)
		}
	}
}

func TestKnownStatus(t *testing.T) {
	for _, s := range []string{"blocked", "done", "idle", "working", "unknown"} {
		if KnownStatus(s) != s {
			t.Errorf("%q not kept", s)
		}
	}
	for _, s := range []string{"", "Blocked", "running", "blocked\x1b[0m", "waiting for you"} {
		if KnownStatus(s) != Unknown {
			t.Errorf("KnownStatus(%q) = %q, want unknown", s, KnownStatus(s))
		}
	}
}

func TestClean(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"plain", "pi-review-pr-7", MaxField, "pi-review-pr-7"},
		{"unicode kept", "π - app", MaxField, "π - app"},
		{"newline and tab", "a\nb\tc\r", MaxField, "abc"},
		{"NUL and bell", "a\x00b\x07c", MaxField, "abc"},
		{"DEL", "a\x7fb", MaxField, "ab"},
		{"C1 bytes", "a\u0085b\u009fc", MaxField, "abc"},
		{"CSI colour", "\x1b[31mred\x1b[0m", MaxField, "red"},
		{"CSI with parameters", "x\x1b[2;10Hy", MaxField, "xy"},
		{"OSC title with BEL", "\x1b]0;evil title\x07name", MaxField, "name"},
		{"OSC title with ST", "\x1b]2;evil\x1b\\name", MaxField, "name"},
		{"two-character escape", "a\x1bcb", MaxField, "ab"},
		{"C1 CSI", "a\u009b31mb", MaxField, "ab"},
		{"bidi override", "safe\u202egnp.exe", MaxField, "safegnp.exe"},
		{"bidi isolate", "a\u2066b\u2069c", MaxField, "abc"},
		{"truncated escape at end", "ok\x1b[3", MaxField, "ok"},
		{"lone ESC at end", "ok\x1b", MaxField, "ok"},
		{"cut at limit", strings.Repeat("a", 100), MaxField, strings.Repeat("a", 77) + "..."},
		{"cut counts characters", strings.Repeat("π", 100), 10, strings.Repeat("π", 7) + "..."},
		{"exact limit", strings.Repeat("a", 80), MaxField, strings.Repeat("a", 80)},
		{"tiny limit", "abcdef", 2, "ab"},
		{"no limit", strings.Repeat("a", 100), 0, strings.Repeat("a", 100)},
		{"invalid utf-8", "a\xffb", MaxField, "a\uFFFDb"},
	}
	for _, c := range cases {
		if got := Clean(c.in, c.limit); got != c.want {
			t.Errorf("%s: Clean(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestCleanFormatCharacters(t *testing.T) {
	cases := map[string]string{
		"zero-width space":    "a\u200bb",
		"left-to-right mark":  "a\u200eb",
		"right-to-left mark":  "a\u200fb",
		"arabic letter mark":  "a\u061cb",
		"byte order mark":     "a\ufeffb",
		"zero-width joiner":   "a\u200db",
		"bidi embedding":      "a\u202ab",
		"bidi override":       "a\u202eb",
		"bidi isolate":        "a\u2066b\u2069",
		"line separator":      "a\u2028b",
		"paragraph separator": "a\u2029b",
		"soft hyphen":         "a\u00adb",
	}
	for name, in := range cases {
		if got := Clean(in, 0); got != "ab" {
			t.Errorf("%s: Clean(%q) = %q, want \"ab\"", name, in, got)
		}
	}
	if got := Clean("π - app 日本", 0); got != "π - app 日本" {
		t.Errorf("ordinary text changed: %q", got)
	}
}

func TestCleanNeverKeepsControlOrFormatCharacters(t *testing.T) {
	var hostile strings.Builder
	for r := rune(0); r < 0x30000; r += 7 {
		hostile.WriteRune(r)
		hostile.WriteString("\x1b[31m\x1b]0;t\x07\x1b")
	}
	for _, r := range Clean(hostile.String(), 0) {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Cf, r) {
			t.Fatalf("character %U survived Clean", r)
		}
	}
}

func TestBuildCleansSandboxOutput(t *testing.T) {
	raw := []Raw{
		{Agent: "pi", AgentStatus: "blocked", Cwd: "/home/u/app/.worktrees/x", Name: "pi-x\x1b]0;pwned\x07"},
		{Agent: "\x1b[2Jclaude", AgentStatus: "rm -rf /", Cwd: "/home/u/app", Name: "claude"},
		{Agent: "pi", AgentStatus: "working", Cwd: "/home/u/app/.worktrees/" + strings.Repeat("w", 200), Name: "long"},
	}
	got := Build("app-1a2b3c4d", "/home/u/app", ".worktrees", raw)
	if got[0].Name != "pi-x" || got[0].Worktree != "x" || got[0].Status != Blocked || got[0].Project != "app" {
		t.Errorf("first row: %+v", got[0])
	}
	if got[1].Kind != "claude" || got[1].Status != Unknown || got[1].Worktree != "-" {
		t.Errorf("second row: %+v", got[1])
	}
	if len(got[2].Worktree) != MaxField {
		t.Errorf("worktree not capped: %d characters", len(got[2].Worktree))
	}
	if got[0].Sandbox != "app-1a2b3c4d" {
		t.Errorf("sandbox = %q", got[0].Sandbox)
	}
}

func TestSortPutsWhatNeedsYouFirst(t *testing.T) {
	agents := []Agent{
		{Project: "b", Name: "pi", Status: Working},
		{Project: "a", Name: "pi-z", Status: Idle},
		{Project: "a", Name: "pi-a", Status: Idle},
		{Project: "a", Name: "pi", Status: Unknown},
		{Project: "c", Name: "pi", Status: Done},
		{Project: "z", Name: "pi", Status: Blocked},
		{Project: "a", Name: "pi-b", Status: Blocked},
	}
	Sort(agents)
	var got []string
	for _, a := range agents {
		got = append(got, a.Status+" "+a.Project+" "+a.Name)
	}
	want := []string{
		"blocked a pi-b", "blocked z pi",
		"done c pi",
		"idle a pi-a", "idle a pi-z",
		"working b pi",
		"unknown a pi",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order:\n got %q\nwant %q", got, want)
	}
}

func TestWriteTable(t *testing.T) {
	var buf bytes.Buffer
	agents := []Agent{
		{Project: "app", Worktree: "review-pr-7", Name: "pi-review-pr-7", Kind: "pi", Status: Blocked},
		{Project: "app", Worktree: "-", Name: "pi", Kind: "", Status: Idle},
	}
	if err := WriteTable(&buf, agents); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.HasPrefix(lines[0], "PROJECT") || !strings.Contains(lines[0], "STATUS") {
		t.Errorf("header = %q", lines[0])
	}
	if f := strings.Fields(lines[1]); !reflect.DeepEqual(f, []string{"app", "review-pr-7", "pi-review-pr-7", "pi", "blocked"}) {
		t.Errorf("row 1 = %q", f)
	}
	if f := strings.Fields(lines[2]); !reflect.DeepEqual(f, []string{"app", "-", "pi", "-", "idle"}) {
		t.Errorf("row 2 = %q", f)
	}
}

func TestDiffTransitions(t *testing.T) {
	a := func(name, status string) Agent {
		return Agent{Sandbox: "s", Project: "app", Name: name, Status: status}
	}
	cases := []struct {
		name       string
		prev, next []Agent
		want       []Event
	}{
		{
			name: "nothing before, an agent is blocked",
			next: []Agent{a("pi", Blocked)},
			want: []Event{{Kind: Appeared, Agent: a("pi", Blocked), Notify: true}},
		},
		{
			name: "nothing before, an agent is idle",
			next: []Agent{a("pi", Idle)},
			want: []Event{{Kind: Appeared, Agent: a("pi", Idle)}},
		},
		{
			name: "working to done notifies",
			prev: []Agent{a("pi", Working)},
			next: []Agent{a("pi", Done)},
			want: []Event{{Kind: Changed, Agent: a("pi", Done), From: Working, Notify: true}},
		},
		{
			name: "working to idle notifies",
			prev: []Agent{a("pi", Working)},
			next: []Agent{a("pi", Idle)},
			want: []Event{{Kind: Changed, Agent: a("pi", Idle), From: Working, Notify: true}},
		},
		{
			name: "working to blocked notifies",
			prev: []Agent{a("pi", Working)},
			next: []Agent{a("pi", Blocked)},
			want: []Event{{Kind: Changed, Agent: a("pi", Blocked), From: Working, Notify: true}},
		},
		{
			name: "idle to working is printed, not notified",
			prev: []Agent{a("pi", Idle)},
			next: []Agent{a("pi", Working)},
			want: []Event{{Kind: Changed, Agent: a("pi", Working), From: Idle}},
		},
		{
			name: "done to idle is printed, not notified",
			prev: []Agent{a("pi", Done)},
			next: []Agent{a("pi", Idle)},
			want: []Event{{Kind: Changed, Agent: a("pi", Idle), From: Done}},
		},
		{
			name: "blocked to working is printed, not notified",
			prev: []Agent{a("pi", Blocked)},
			next: []Agent{a("pi", Working)},
			want: []Event{{Kind: Changed, Agent: a("pi", Working), From: Blocked}},
		},
		{
			name: "same status, nothing",
			prev: []Agent{a("pi", Blocked)},
			next: []Agent{a("pi", Blocked)},
		},
		{
			name: "an agent disappears",
			prev: []Agent{a("pi", Working)},
			want: []Event{{Kind: Gone, Agent: a("pi", Working)}},
		},
		{
			name: "an agent appears while another goes",
			prev: []Agent{a("old", Working)},
			next: []Agent{a("new", Blocked)},
			want: []Event{ // sorted by sandbox and name
				{Kind: Appeared, Agent: a("new", Blocked), Notify: true},
				{Kind: Gone, Agent: a("old", Working)},
			},
		},
		{
			name: "same agent name in another sandbox is another agent",
			prev: []Agent{a("pi", Working)},
			next: []Agent{{Sandbox: "t", Project: "app", Name: "pi", Status: Working}},
			want: []Event{
				{Kind: Gone, Agent: a("pi", Working)},
				{Kind: Appeared, Agent: Agent{Sandbox: "t", Project: "app", Name: "pi", Status: Working}},
			},
		},
	}
	for _, c := range cases {
		got := Diff(c.prev, c.next)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestCarryKeepsRowsOfFailedSandboxes(t *testing.T) {
	prev := []Agent{{Sandbox: "a", Name: "pi", Status: Working}, {Sandbox: "b", Name: "pi", Status: Working}}
	next := []Agent{{Sandbox: "b", Name: "pi", Status: Blocked}}
	if got := Carry(prev, next, nil); !reflect.DeepEqual(got, next) {
		t.Errorf("no failures: %+v", got)
	}
	got := Carry(prev, next, map[string]bool{"a": true})
	want := []Agent{{Sandbox: "b", Name: "pi", Status: Blocked}, {Sandbox: "a", Name: "pi", Status: Working}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a timed out:\n got %+v\nwant %+v", got, want)
	}
	if len(next) != 1 {
		t.Error("Carry changed its input")
	}
}

func TestDesktopCommand(t *testing.T) {
	none := func(string) bool { return false }
	has := func(p string) bool { return p == "notify-send" || p == "osascript" }
	cases := []struct {
		name       string
		configured []string
		goos       string
		have       func(string) bool
		want       []string
	}{
		{"configured wins everywhere", []string{"sh", "-c", "x", "notify"}, "linux", has, []string{"sh", "-c", "x", "notify"}},
		{"linux with notify-send", nil, "linux", has, []string{"notify-send"}},
		{"linux without notify-send", nil, "linux", none, nil},
		{"macOS", nil, "darwin", has, []string{"osascript", "-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run"}},
		{"macOS without osascript", nil, "darwin", none, nil},
		{"windows: bell only", nil, "windows", has, nil},
	}
	for _, c := range cases {
		if got := DesktopCommand(c.configured, c.goos, c.have); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	configured := []string{"ntfy"}
	DesktopCommand(configured, "linux", has)[0] = "changed"
	if configured[0] != "ntfy" {
		t.Error("DesktopCommand returned the configured slice itself")
	}
}

func TestDescribeAndNotice(t *testing.T) {
	blocked := Agent{Project: "app", Worktree: "review-pr-7", Name: "pi-review-pr-7", Status: Blocked}
	e := Event{Kind: Changed, Agent: blocked, From: Working, Notify: true}
	if got := Describe(e); got != "app/review-pr-7: pi-review-pr-7 working -> blocked" {
		t.Errorf("Describe = %q", got)
	}
	if got := Describe(Event{Kind: Gone, Agent: Agent{Project: "app", Worktree: "-", Name: "pi"}}); got != "app: pi is gone" {
		t.Errorf("Describe gone = %q", got)
	}
}

func TestNoticeCoalescesOnePoll(t *testing.T) {
	blocked := func(name string) Event {
		return Event{Kind: Changed, Agent: Agent{Name: name, Status: Blocked}, Notify: true}
	}
	cases := []struct {
		name   string
		events []Event
		want   string
	}{
		{"one agent", []Event{blocked("pi-review-7")}, "pi-review-7 needs you"},
		{"one done", []Event{{Kind: Changed, Agent: Agent{Name: "pi", Status: Done}, Notify: true}}, "pi finished"},
		{"one idle", []Event{{Kind: Changed, Agent: Agent{Name: "pi", Status: Idle}, Notify: true}}, "pi is idle"},
		{"several blocked", []Event{blocked("a"), blocked("b"), blocked("c")}, "3 agents need you"},
		{"mixed", []Event{blocked("a"), {Agent: Agent{Name: "b", Status: Done}, Notify: true}}, "2 agents need attention"},
	}
	for _, c := range cases {
		title, msg := Notice(c.events)
		if title != "airlock" || msg != c.want {
			t.Errorf("%s: Notice = %q, %q; want %q", c.name, title, msg, c.want)
		}
	}
}

func TestComposeNotifySend(t *testing.T) {
	cases := []struct {
		name    string
		prefix  []string
		title   string
		message string
		want    []string
	}{
		{"notify-send gets -- and escaped markup", []string{"notify-send"}, "-x <b>", "a & <i>",
			[]string{"notify-send", "--", "-x &lt;b&gt;", "a &amp; &lt;i&gt;"}},
		{"full path is recognised", []string{"/usr/bin/notify-send", "-u", "low"}, "t", "m",
			[]string{"/usr/bin/notify-send", "-u", "low", "--", "t", "m"}},
		{"a configured command gets plain arguments", []string{"sh", "-c", "x", "notify"}, "-x <b>", "a & b",
			[]string{"sh", "-c", "x", "notify", "-x <b>", "a & b"}},
		{"osascript gets plain arguments", []string{"osascript", "-e", "end run"}, "t", "m",
			[]string{"osascript", "-e", "end run", "t", "m"}},
	}
	for _, c := range cases {
		if got := Compose(c.prefix, c.title, c.message); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if Compose(nil, "t", "m") != nil {
		t.Error("an empty command must compose to nothing")
	}
	prefix := []string{"notify-send"}
	_ = Compose(prefix, "t", "m")
	if len(prefix) != 1 {
		t.Error("Compose changed its input")
	}
}
