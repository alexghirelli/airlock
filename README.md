# airlock

A sandbox launcher for AI coding agents. Run `airlock` in a project folder and it
starts (or resumes) a dedicated, hardened container for that project and attaches
you to it, with [Pi](https://pi.dev) and [Herdr](https://herdr.dev) ready to work.

Agents work freely inside the box. By default they cannot reach your home
directory, your local network, your git credentials or the Docker socket.

> **Status: MVP.** The core is implemented and covered by automated tests (below).
> Several features from the design are not built yet; see [What is not there yet](#what-is-not-there-yet).

## Install

You need Docker (Engine, Docker Desktop, OrbStack or Colima) running.

**Linux and macOS**, one line (checks the download against the release checksums):

```sh
curl -fsSL https://raw.githubusercontent.com/alexghirelli/airlock/main/install.sh | sh
```

It installs `airlock` into `~/.local/bin` (set `AIRLOCK_INSTALL_DIR` to change that,
`AIRLOCK_VERSION=v0.1.0` to pin a release) and tells you if that folder is not in
your `PATH`. To read the script before running it, open
[install.sh](install.sh) first.

**Manually**: download the archive for your system from the
[Releases](https://github.com/alexghirelli/airlock/releases) page, check it with
`sha256sum -c --ignore-missing SHA256SUMS`, and put `airlock` somewhere in your `PATH`.
On **Windows**, use the `.zip`, preferably from inside WSL2 (see
`airlock doctor` for the trade-offs).

**With Go**: `go install github.com/alexghirelli/airlock/cmd/airlock@latest`.

**From source**: `go build -o airlock ./cmd/airlock` in a clone of the repository.

There is no published container image: the first `airlock` in any project builds the
image on your machine (about two minutes, 1.3 GB, downloads Pi, Herdr and mise) and
every later project reuses it.

## Quick start

```sh
cd ~/projects/my-app
airlock                              # first run builds the image
```

Inside, Herdr opens with Pi running in the project's workspace. Close the terminal
at any time: the sandbox and its agents keep running, and `airlock` reattaches.

```
airlock [path]               start or resume the sandbox for a project and attach
airlock shell [path]         open a shell in it
airlock exec [path] -- cmd   run a command in it
airlock stop|rm [path]       stop it (tools stay) / delete it
airlock wt add|open|focus|rm | airlock review <change>   worktrees and Gerrit reviews
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
- Git worktrees share the project's sandbox, each as a Herdr workspace, or get a sandbox
  of their own (see [Worktrees](#worktrees-parallel-work-and-gerrit-reviews)).
- Network `open-no-lan` (default): the internet works; your computer, your LAN,
  link-local/metadata addresses and other sandboxes are blocked by a firewall
  that the agent cannot change (see [Security model](#security-model)).
- `sudo` works inside, so agents can `apt install` and use `mise` for
  Node/Python/Go/Ruby. Installed tools persist until `airlock rm`.
- Pi logins and settings live in a Docker volume, shared by all projects
  (`credentials.scope = "shared"`) or per project.

## Worktrees: parallel work and Gerrit reviews

Work on several branches at once, one git worktree each, and review Gerrit patches
next to your own feature. By default every worktree shows up as a **Herdr workspace in
the same session**, so you switch between them without opening terminals.

```sh
airlock wt add rate-limit "rate limiting of the API, only includes/api/"
airlock review 12345                       # the latest patchset of Gerrit change 12345
airlock review https://gerrit.example.org/r/c/core/+/12345/3 "check the SQL"
airlock wt                                 # worktrees, branches, changes, sandboxes, focus
airlock wt open rate-limit                 # come back to one
airlock wt focus rate-limit "now: the tests"   # change what its agent concentrates on
airlock wt rm review-12345                 # remove the worktree, keep the branch
```

- `add` creates the worktree under `.worktrees/` (git-excluded) on a new branch from
  `HEAD` (`-b` to name it or use an existing or remote branch, `--from` to start
  elsewhere). `review` fetches `refs/changes/NN/12345/P` **on the host**, where your
  Gerrit credentials are, and checks it out on a **local branch** `review/12345`, so
  a review can include changes of your own. Run it again and the branch moves to the
  newest patchset, unless that would lose uncommitted changes or local commits: then it
  says so and prints the `git rebase --onto` to run. The remote is `gerrit` if the
  repository has one, otherwise `origin`; `git.gerrit_remote` changes it.
- Both start the sandbox and attach. From another terminal, `--no-attach` adds the
  workspace to the Herdr session that is already open.
- **A task per worktree.** Text after the name or change is the worktree's *focus*.
  A review also gets a task of its own, written from the fetched commit: the change,
  the patchset, the subject and the author, with the rules of a review (no push, no
  edits unless asked, findings by file) and a pointer to the Gerrit tools for comments.
  Pi reads both every time it starts in the worktree, so every new session there has
  its scope, and `wt focus` applies to the next one. They are files in the worktree's
  git directory, not in the checkout, so they never show in `git status`.

### Keeping a worktree apart

A sandbox is the boundary, not a folder: agents in the same sandbox can read and write
each other's worktrees, and the project's untracked files (a `LocalSettings.php` with
database credentials, say). That is fine for your own branches; it is a risk for a
patch from someone else, because instructions hidden in it may steer the agent that
reads it. So a worktree can have a sandbox of its own:

```toml
[sandbox]
worktree_isolation = "none"      # none (default) | reviews | all
```

| Value | `wt add` | `review` |
|---|---|---|
| `none` | project sandbox, Herdr workspace | project sandbox, Herdr workspace |
| `reviews` | project sandbox | **own sandbox** |
| `all` | own sandbox | own sandbox |

`--shared` and `--isolated` override the setting for one worktree. An isolated worktree
lives in `.worktrees/.isolated/`, mounts only itself and the repository's `.git`
(hooks, config and its own `config.worktree` read-only), and has its own terminal and
Herdr: the main checkout, its untracked files and every other worktree are not visible,
and a port used there does not clash with yours. The project's sandbox sees
`.worktrees/.isolated/` as an empty read-only folder.

- `airlock` run inside a worktree resolves to the sandbox it belongs to; that means
  `airlock stop|rm` in a shared worktree acts on the project's sandbox.
- A worktree builds the **main repository's** configuration and project image, never its
  own: a patch under review cannot change how its own sandbox is built.
- Worktrees made by `airlock wt` are locked, so a `git worktree prune` run where they
  look missing (the project's sandbox does not see isolated ones) cannot forget them.
- Agents can make worktrees under `.worktrees/` themselves; they land in the project's
  sandbox. Isolated ones are made with `airlock wt` on the host.

What worktrees still share is the git directory: objects, branches and tags. An agent in
an isolated worktree can rewrite refs of other branches. It cannot read or change their
files. See [Security model](#security-model).

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
VAULT_DIR = { value = "/mnt/vault" }           # a literal value, not a secret

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

[[mcp]]                                        # remote MCP server
name = "docs"
url = "https://example.com/mcp"
headers = { Authorization = "DOCS_MCP_AUTH" }  # host env var holding the value

[[mcp]]                                        # local (stdio) MCP server
name = "tracker"
command = "node"
args = ["/mnt/tools/mcp-tracker/src/index.js"] # code comes in through a mount
env = { TRACKER_USER = "alice" }
env_from_host = { TRACKER_TOKEN = "TRACKER_TOKEN" }   # secrets stay out of the file

[[setup]]                                      # preparation run inside the sandbox
name = "gerrit-venv"
run = """
python -m venv ~/.local/share/mise/venvs/gerrit &&
~/.local/share/mise/venvs/gerrit/bin/pip install -r /mnt/tools/gerrit/requirements.txt
"""
creates = "~/.local/share/mise/venvs/gerrit/bin/python"
watch = ["/mnt/tools/gerrit"]                  # rerun when files there change
```

`[[setup]]` runs a shell command at start, before the agent, for anything airlock
knows nothing about (a virtualenv, `npm ci`, a copy into a writable folder). A step
is skipped while its command is unchanged and the `creates` path exists; a failing
step is reported by `airlock` and retried at the next start, without stopping the
sandbox. Only your user config may define steps: a project's `.airlock.toml` cannot.
Put what they build under `~/.local/share/mise` to keep it across recreations.
With `watch`, a step also reruns when the files under those paths change (size or
modification time; `.git`, `__pycache__`, `node_modules` and `*.log` are ignored),
so a copy of a tool from a mount stays current.

The configuration is read when the sandbox starts. If you change it while a sandbox
is running, `airlock` says so; `airlock restart` stops and starts it again to apply
the change (sessions inside it end).

Already have `mcpServers` JSON from another tool? `airlock mcp import file.json`
prints the matching `[[mcp]]` entries. It never copies values that look like secrets
(it points to host variables instead) and warns about paths on your computer,
virtualenvs and runtimes the sandbox does not have yet.

A local MCP server runs inside the Linux sandbox, so its code must be mounted
and its runtime must exist there: add `extras.mise = ["node@22", "python@3.12"]`
as needed. A virtualenv or compiled `node_modules` built on macOS will not run
in Linux; create them from inside the sandbox instead.

Every agent also gets a built-in `concise` skill (`image/skills/concise/SKILL.md`):
lead with the answer, no preamble or recap, numbered steps, one tangent at a time.
Its rules are written into the generated `/AGENTS.md` and Pi loads the skill too.
Turn it off with `[agents] concise = false`. It was inspired by
[i-have-adhd](https://github.com/ayghri/i-have-adhd) (MIT), but is its own, shorter text.

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
- **Worktrees in the same sandbox are not kept apart.** By default they all share the
  project's sandbox, so an agent reviewing a patch can read and change your other
  worktrees and the project's untracked files. Set `sandbox.worktree_isolation` to
  give reviews (or every worktree) a sandbox of their own.
- **Worktrees share one git directory**, isolated or not. An agent in one can create,
  move or delete branches and tags that the others (and you) see, and add objects to
  the shared store. Review what a sandbox committed before you push it.
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
| `[[mcp]]` | Written into Pi's MCP config (remote and stdio servers); the entry and the mounted command are tested, but not a full session against a real MCP server. |
| A published container image | The image is built locally on first run. |
| Windows and macOS | The release archives compile for them, but airlock has only been run on Linux. |

## Development

```sh
go test ./...                                                # unit tests, a few seconds
go test -tags integration -timeout 15m ./tests/...           # needs Docker and internet
AIRLOCK_IT_SLOW=1 go test -tags integration -run Packages ./tests/...   # the slow, network-heavy test
```

The integration tests build the image once (about three minutes the first time) and
then take about a minute. They cover: isolation (with a positive control, so a
missing firewall would be noticed), capability dropping, ownership, protected git
files, worktrees (one sandbox each, none seeing another), untrusted project config, same-name projects, mounts,
persistence, Herdr attach/detach/reattach through a real pseudo-terminal,
`.airlockignore`, `audit`, git modes and `doctor`. The test that installs Pi
packages and Claude Code is opt-in (`AIRLOCK_IT_SLOW=1`).

CI runs only the fast checks on every push (formatting, vet, unit tests,
cross-compilation). The integration tests run weekly, and on demand from the
Actions tab (the `integration` workflow).

The tests set `AIRLOCK_PRUNE_IMAGES=0` so the several image variants they build
survive each other. Normally airlock removes superseded `airlock:*` tags after a
build; the image is about 1.3 GB (1.8 GB with Claude Code).

### Releasing

Releases come from annotated tags, and the tag message is the release text:

```sh
git tag -a v0.2.0 -F notes.md   # a summary line, then what changed, for users
git push origin v0.2.0
```

The `release` workflow refuses lightweight tags and messages without a summary
line and at least one line of detail. The release is named after the version only
(`v0.2.0`); the tag message is its text. It builds the archives, publishes them with
`SHA256SUMS`, and appends the install line and a link to the full changelog.

## Licence

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

The image downloads Pi (MIT), Herdr (Apache-2.0), mise and (only when a configured package needs it)
Claude Code at build time; each keeps its own licence and terms. Claude Code is
proprietary, so a published image must not bundle it.
