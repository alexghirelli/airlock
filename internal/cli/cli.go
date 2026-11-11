// Package cli implements the airlock command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alexghirelli/airlock/internal/backend"
	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/termio"
	"github.com/alexghirelli/airlock/internal/version"
)

const usage = `airlock — a sandbox for AI coding agents

Usage:
  airlock [path]               start or resume the sandbox for a project and attach
  airlock --team [path]        same, and start the agent team
  airlock shell [path]         open a shell in the sandbox
  airlock exec [path] -- cmd   run a command in the sandbox
  airlock stop [path]          stop the sandbox (installed tools are kept)
  airlock restart [path]       stop and start again, to apply a changed config
  airlock rm [path]            delete the sandbox
  airlock wt add <name> [focus...]   a worktree on a new or existing branch (-b, --from)
  airlock review <change> [focus...] a Gerrit patchset on a local branch review/<change>
                               (12345, 12345/3 or the change URL); same as wt review
  airlock wt open <name>       come back to a worktree
  airlock wt focus <name> [text]  show / set / clear (text "") what its agent concentrates on
  airlock wt [ls] | rm <name>  list worktrees / remove one (--force with changes)
  airlock ls                   list sandboxes
  airlock login [--reset]      log in to the configured providers
  airlock build | update       build the image / rebuild with the latest pinned versions
                               (build --if-missing skips an image that already exists)
  airlock doctor               check the runtime, mounts, firewall and credentials
  airlock audit [path]         report changes to host-executable files (--accept to acknowledge)
  airlock config [init|show|path|edit]
  airlock mcp import <file>    turn an "mcpServers" JSON file into [[mcp]] entries
  airlock version

The path defaults to the git root of the current directory.

Flags:
  --team             start the agent team (herdr UI)
  --ui <ui>          herdr | pi | shell
  --network <mode>   open-no-lan | allowlist | open
  --ephemeral        throw the sandbox away when the session ends
  --recreate         recreate the sandbox if its fixed settings changed
  -b, --branch <b>   wt add/review: the branch to use (default: the name / review/<change>)
  --from <ref>       wt add: where a new branch starts (default HEAD)
  --no-attach        wt add/review/open: prepare the sandbox but do not attach
  --attach           wt add/review: attach even if a Herdr is already open in another terminal
  --shared           wt add/review: use the project's sandbox, a Herdr workspace each
  --isolated         wt add/review: give the worktree a sandbox of its own
                     (the default comes from sandbox.worktree_isolation)
  --remote <host>    run the command on a remote machine over ssh
  --config <file>    use this user config file
  --json             machine-readable output (ls, doctor, audit)
  --verbose, -v      show image build output and details
  --help, -h         show this help
`

// commands maps a first word to a command; anything else is a project path.
var commands = map[string]bool{
	"wt": true, "review": true, "shell": true, "exec": true, "stop": true, "restart": true, "mcp": true, "rm": true, "ls": true, "login": true,
	"build": true, "update": true, "doctor": true, "audit": true, "config": true,
	"version": true, "help": true,
}

// args is a parsed command line.
type args struct {
	command   string   // "start" if none given
	path      string   // optional project path
	rest      []string // arguments after `--`, or sub-arguments (config init)
	json      bool
	verbose   bool
	team      bool
	teamSet   bool
	ui        string
	network   string
	ephemer   bool
	recreate  bool
	remote    string
	cfgFile   string
	reset     bool
	force     bool
	accept    bool
	ifMissing bool
	noAttach  bool
	attach    bool
	shared    bool
	isolated  bool
	branch    string
	from      string
	help      bool
}

func parseArgs(argv []string) (*args, error) {
	a := &args{command: "start"}
	var positional []string
	commandSeen := false
	value := func(i *int, name, inline string, hasInline bool) (string, error) {
		if hasInline {
			return inline, nil
		}
		if *i+1 >= len(argv) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return argv[*i], nil
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			a.rest = append(a.rest, argv[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name, inline, hasInline := strings.Cut(arg, "=")
			var err error
			switch name {
			case "--json":
				a.json = true
			case "--verbose", "-v":
				a.verbose = true
			case "--team":
				a.team, a.teamSet = true, true
			case "--ephemeral":
				a.ephemer = true
			case "--recreate":
				a.recreate = true
			case "--reset":
				a.reset = true
			case "--force":
				a.force = true
			case "--accept":
				a.accept = true
			case "--if-missing":
				a.ifMissing = true
			case "--no-attach":
				a.noAttach = true
			case "--attach":
				a.attach = true
			case "--shared":
				a.shared = true
			case "--isolated":
				a.isolated = true
			case "--branch", "-b":
				a.branch, err = value(&i, name, inline, hasInline)
			case "--from":
				a.from, err = value(&i, name, inline, hasInline)
			case "--help", "-h":
				a.help = true
			case "--ui":
				a.ui, err = value(&i, name, inline, hasInline)
			case "--network":
				a.network, err = value(&i, name, inline, hasInline)
			case "--remote":
				a.remote, err = value(&i, name, inline, hasInline)
			case "--config":
				a.cfgFile, err = value(&i, name, inline, hasInline)
			default:
				return nil, fmt.Errorf("unknown flag %s (see airlock --help)", name)
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		if !commandSeen && len(positional) == 0 && commands[arg] {
			a.command = arg
			commandSeen = true
			continue
		}
		positional = append(positional, arg)
	}
	if a.command == "review" { // airlock review 12345 is airlock wt review 12345
		a.command = "wt"
		positional = append([]string{"review"}, positional...)
	}
	switch a.command {
	case "config", "login", "mcp", "wt":
		a.rest = append(positional, a.rest...)
	default:
		if len(positional) > 1 {
			return nil, fmt.Errorf("unexpected argument %q", positional[1])
		}
		if len(positional) == 1 {
			a.path = positional[0]
		}
	}
	return a, nil
}

// env bundles what every command needs.
type env struct {
	stdout, stderr io.Writer
	a              *args
}

func (e *env) errf(format string, v ...any) { fmt.Fprintf(e.stderr, format+"\n", v...) }

// Main runs the CLI and returns the process exit code.
func Main(argv []string, stdout, stderr io.Writer) int {
	a, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintf(stderr, "airlock: %v\n", err)
		return 2
	}
	e := &env{stdout: stdout, stderr: stderr, a: a}
	if a.help || a.command == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if a.command == "version" {
		fmt.Fprintf(stdout, "airlock %s\n", version.Version)
		return 0
	}
	if a.remote != "" {
		return runRemote(e, argv)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	code, err := e.run(ctx)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				fmt.Fprintf(stderr, "airlock: %s\n", ee.msg)
			}
			return ee.code
		}
		fmt.Fprintf(stderr, "airlock: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// exitError carries a specific exit code (and optionally a message).
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func (e *env) run(ctx context.Context) (int, error) {
	switch e.a.command {
	case "start":
		return e.cmdStart(ctx)
	case "shell":
		return e.cmdShell(ctx)
	case "exec":
		return e.cmdExec(ctx)
	case "stop":
		return e.cmdStop(ctx)
	case "restart":
		return e.cmdRestart(ctx)
	case "mcp":
		return e.cmdMCP()
	case "rm":
		return e.cmdRm(ctx)
	case "ls":
		return e.cmdLs(ctx)
	case "build", "update":
		return e.cmdBuild(ctx)
	case "config":
		return e.cmdConfig()
	case "login":
		return e.cmdLogin(ctx)
	case "doctor":
		return e.cmdDoctor(ctx)
	case "audit":
		return e.cmdAudit(ctx)
	case "wt":
		return e.cmdWt(ctx)
	}
	return 2, fmt.Errorf("unknown command %q", e.a.command)
}

// loaded is a resolved project with its configuration.
type loaded struct {
	project *sandbox.Project
	res     *config.Result
}

func (e *env) flags() config.Flags {
	f := config.Flags{UI: e.a.ui, Network: e.a.network, Ephemeral: e.a.ephemer}
	if e.a.teamSet {
		t := e.a.team
		f.Team = &t
	}
	return f
}

// load resolves the project and loads the layered configuration.
func (e *env) load() (*loaded, error) {
	p, err := sandbox.Resolve(e.a.path)
	if err != nil {
		return nil, err
	}
	res, err := config.Load(config.LoadOptions{ProjectDir: p.RepoRoot(), UserConfigPath: e.a.cfgFile, Flags: e.flags()})
	if err != nil {
		return nil, err
	}
	// Which sandbox a worktree uses depends on where the configuration puts them.
	return &loaded{project: p.Settle(res.Config.Sandbox.WorktreesDir), res: res}, nil
}

// loadUser loads the configuration without a project.
func (e *env) loadUser() (*config.Result, error) {
	return config.Load(config.LoadOptions{UserConfigPath: e.a.cfgFile, Flags: e.flags()})
}

func newBackend(c *config.Config) (backend.Backend, error) {
	switch c.Sandbox.Backend {
	case "container", "gvisor":
		return backend.NewDocker(c.Sandbox.Backend)
	}
	return nil, fmt.Errorf("the %q backend is not implemented yet (available: container, gvisor)", c.Sandbox.Backend)
}

// manager builds the Manager for a loaded project.
func (e *env) manager(l *loaded) (*sandbox.Manager, error) {
	b, err := newBackend(&l.res.Config)
	if err != nil {
		return nil, err
	}
	return &sandbox.Manager{
		B: b, Config: l.res, Project: l.project, Host: sandbox.DefaultHost(),
		Log: e.stderr, Verbose: e.a.verbose, Recreate: e.a.recreate, Confirm: e.confirm,
	}, nil
}

// confirm asks a yes/no question on the terminal; without one the answer is no.
func (e *env) confirm(prompt string) bool {
	if !termio.IsTerminal() {
		return false
	}
	fmt.Fprintf(e.stderr, "airlock: %s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Fscanln(os.Stdin, &ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}
