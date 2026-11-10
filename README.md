# airlock

A sandbox launcher for AI coding agents. Run `airlock` in a project folder and it
starts (or resumes) a dedicated, hardened container for that project and attaches
you to it, with [Pi](https://pi.dev) and [Herdr](https://herdr.dev) ready to work.

Agents work freely inside the box. By default they cannot reach your home
directory, your local network, your git credentials or the Docker socket.

> **Status: MVP.** The core is implemented and covered by automated tests (below).
> Several features from the design are not built yet; see [What is not there yet](#what-is-not-there-yet).

## Quick start

Requirements: Docker (Engine, Docker Desktop, OrbStack, Colima) and, to build from
source, the Go version in `go.mod`.

```sh
go build -o airlock ./cmd/airlock    # no install script or release binaries yet
cd ~/projects/my-app
airlock                              # first run builds the image (a few minutes)
```

Inside, Herdr opens with Pi running in the project's workspace. Close the terminal
at any time: the sandbox and its agents keep running, and `airlock` reattaches.

```
airlock [path]               start or resume the sandbox for a project and attach
airlock shell [path]         open a shell in it
airlock exec [path] -- cmd   run a command in it
airlock stop|rm [path]       stop it (tools stay) / delete it
airlock ls                   list sandboxes
airlock login                log in to the providers named in your config
airlock audit [path]         report changes to files that run on your computer
airlock doctor               check the runtime, the firewall and the setup
airlock build | update       build the image / rebuild it from scratch
airlock config init|show|path|edit
airlock --remote <ssh-host> <command>
```

## What the sandbox is

- One container per project (`airlock-<name>-<hash>`), persistent by default.
- The project is mounted **at the same absolute path** as on the host, so git
  worktrees (which store absolute paths) work from both sides. Files you and the
  agent create are owned by you.
- Network `open-no-lan` (default): the internet works; your computer, your LAN,
  link-local/metadata addresses and other sandboxes are blocked by a firewall
  that the agent cannot change (see [Security model](#security-model)).
- `sudo` works inside, so agents can `apt install` and use `mise` for
  Node/Python/Go/Ruby. Installed tools persist until `airlock rm`.
- Pi logins and settings live in a Docker volume, shared by all projects
  (`credentials.scope = "shared"`) or per project.

## Configuration

`airlock config init` writes a fully commented template to
`$XDG_CONFIG_HOME/airlock/config.toml` (`%APPDATA%\airlock\config.toml` on
Windows). Precedence: defaults < user config < `<project>/.airlock.toml` < CLI
flags < `AIRLOCK_*` environment variables.

The project file comes with the repository, so it is **untrusted**. It may only
set the UI, the default model, whether the team is on, extra `apt`/`mise`
packages, a project Dockerfile, resource limits (lower only) and a *stricter*
network mode. Mounts, environment variables, credentials, capabilities and
anything that loosens the network are ignored, and the ignored keys are reported.

Useful pieces:

```toml
[pi]
packages = ["npm:pi-claude-bridge@0.11.0"]   # installs Claude Code in the image too
default_model = "claude-bridge/claude-opus-5-5"

[env]                                          # passed from your environment
OPENCODE_API_KEY = { from_host_env = "OPENCODE_API_KEY" }

[[mount]]                                      # shared folders, at /mnt/<name>
name = "notes"
host = "~/notes"
mode = "rw"
description = "My knowledge base. Read its README first."

[[mount]]
name = "skills"
host = "~/agent-skills"
mode = "ro"
skills = "."                                   # load Pi skills from here
```

Agents are told about the sandbox, the network policy, git rules and every mount
through a generated `/AGENTS.md`. Verify model ids with `pi --list-models` inside
the sandbox rather than assuming them.

`.airlockignore` (gitignore syntax, in the project root) hides files from the
sandbox: matching files appear empty and read-only. Files created later are not
hidden until the sandbox is recreated.

### Example setups

1. **Anthropic API key**: `default_model = "anthropic/<id>"`, run `airlock login`.
2. **Claude subscription**: `packages = ["npm:pi-claude-bridge@0.11.0"]`,
   `default_model = "claude-bridge/<id>"`, run `airlock login` (it runs
   `claude auth login`; the bridge uses Claude Code's login, not Pi's).
3. **API key from the environment**: add it under `[env]` and export it on the host.

## Security model

What it protects against: an agent that is prompt-injected, buggy or overeager
trying to (a) read or change host files outside the project, (b) reach your host or
LAN (router, NAS, Docker socket, cloud metadata), (c) leave code that later runs on
your computer, or (d) get at your credentials.

How:

- Non-root user, `--cap-drop ALL` plus a small set, never privileged, no host
  network/PID/IPC, **no Docker socket**, only the project and declared mounts.
- The egress firewall is installed by the entrypoint from settings fixed at
  container creation. `docker exec` gives processes the container's capabilities,
  not the entrypoint's, so every session airlock starts goes through a wrapper
  that removes `NET_ADMIN`/`NET_RAW` from the bounding set: `sudo iptables -F`
  fails. (This is tested; it was a real flaw in the first design.)
- `.git/hooks` and `.git/config` are mounted read-only, which blocks the usual
  ways to plant code that git later runs on the host.
- No git credentials in the box by default. `git.mode = "push"` injects one token
  from a host variable, offered only to the configured hosts. `~/.ssh` is never
  mounted.
- `airlock audit` reports changes to hooks, `.envrc`, `.vscode/`, build and CI
  files, `package.json`/`composer.json` scripts and new executables. It reports; it
  does not revert. The baseline moves only when you run `airlock audit --accept`.

What it does **not** protect against:

- **A container shares the host kernel.** A kernel exploit by an agent with `sudo`
  can escape. A stronger backend (gVisor, microVM) is on the roadmap, not here.
- Anything you hand over: a read-write mount can be modified, a mounted `~/.aws`
  is readable, a `push` token can push, environment variables are visible to every
  process in the box (and in `docker inspect`).
- With `credentials.scope = "shared"`, any sandbox on the machine can read the
  provider logins. `per-project` isolates them. `airlock login --reset` wipes them.
- The toolchain volume is shared between projects.
- The firewall allows DNS to the resolvers Docker forwards to (needed for name
  resolution), so DNS can carry data out. Public internet access can exfiltrate
  anything the agent can read, including your source. Hide secrets with
  `.airlockignore`; `airlock doctor` warns about visible secret-looking files.
- Rootless Docker/Podman: file ownership of the project may not map to your user
  (`doctor` says so).

## What is not there yet

| Not implemented | Notes |
|---|---|
| `network.mode = "allowlist"` | Fails with a clear message. Needs a filtering proxy. |
| `--team` | The Herdr bootstrap and team scripts are written, but not verified end to end. Treat as experimental. |
| `microvm` / `vm` backends | Rejected by the CLI. `gvisor` starts the container with `runsc` but has not been run through the test suite. |
| `airlock login` | Runs Pi's and Claude Code's own login flows; checked up to the credential prompt only (no real login was made). `--reset` is untested. |
| `[[mcp]]` | Written into Pi's MCP config; not tested against a real server. |
| Release binaries, install script, CI, published image | Build from source for now. |
| Windows and macOS | Written to be portable, tested only on Linux. |

## Development

```sh
go test ./...                                                # unit tests
go test -tags integration -timeout 30m ./tests/...           # needs Docker and internet
```

The integration tests build the image and cover: isolation (with a positive
control, so a missing firewall would be noticed), capability dropping, ownership,
protected git files, worktrees, untrusted project config, same-name projects,
mounts, persistence, Pi packages, Herdr attach/detach/reattach through a real
pseudo-terminal, `.airlockignore`, `audit`, git modes and `doctor`.

The integration tests use disk (the image is about 1.3 GB, 1.8 GB with Claude
Code). airlock removes superseded `airlock:*` image tags after each build.

## Licence

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

The image downloads Pi, Herdr, mise and (only when a configured package needs it)
Claude Code at build time; each keeps its own licence and terms. Claude Code is
proprietary, so a published image must not bundle it.
