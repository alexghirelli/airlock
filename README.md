# airlock

One hardened container per project for AI coding agents, with parallel git worktrees
and patch reviews from Gerrit, GitHub, GitLab or Bitbucket. Nothing on your machine
but Docker. Run `airlock` in a project folder and it starts (or resumes) a dedicated
container for that project and attaches you to it, with [Pi](https://pi.dev) and
[Herdr](https://herdr.dev) ready to work.

![airlock: a prompt-injected agent cannot reach the host's secrets, the LAN, the firewall or the git hooks; then a pull request is reviewed in a second Herdr workspace](assets/demo.gif)

Agents work freely inside the box. By default they cannot reach your home
directory, your local network, your git credentials or the Docker socket. They can
reach the internet, so anything they can read in the project they could also send
out: airlock limits the damage to what you put in the box, it does not make a
compromised agent harmless (see [Security model](#security-model)).

> **Status: early.** The core is implemented, covered by automated tests (below) and
> used daily on Linux and macOS (Docker Desktop). Some features from the design are
> not built yet; see [What is not there yet](#what-is-not-there-yet).

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

The first `airlock` in any project downloads the base image (about 300 MB, a few
seconds to a minute; it is published to GHCR for every release, for amd64 and
arm64) and every later project reuses it. The image has Pi, Herdr and mise and
nothing proprietary: other agents are installed on your machine, by their own
installers, the first time you use them (see [Agents](#agents-use-the-one-you-prefer)).
If the download fails, or for a development build,
airlock builds the image locally instead (about two minutes, 1.3 GB). Set
`AIRLOCK_BUILD_LOCAL=1` to always build locally, and `airlock build` to do it now.

## Quick start

```sh
cd ~/projects/my-app
airlock                              # first run builds the image
```

Inside, Herdr opens with Pi running in the project's workspace. Close the terminal
at any time: the sandbox and its agents keep running, and `airlock` reattaches.

New to airlock? `airlock init` in the project folder reads what the project uses and
writes a starter `.airlock.toml` (see [Starting a project](#starting-a-project-airlock-init)).

```
airlock [path]               start or resume the sandbox for a project and attach
airlock shell [path]         open a shell in it
airlock exec [path] -- cmd   run a command in it
airlock stop|rm [path]       stop it (tools stay) / delete it (--purge: and its own logins)
airlock wt add|open|focus|handoff|rm | airlock review <change>   worktrees and Gerrit reviews
airlock summary [worktree]   what a worktree changed, before you trust or merge it (--json)
airlock ls                   list sandboxes
airlock inbox [--watch]      the agents that need you, across sandboxes (--watch: notify)
airlock gc [--force]         list (--force: remove) what deleted projects and old releases left behind
airlock login                log in to the providers named in your config
airlock audit [path]         report changes to files that run on your computer
airlock doctor               check the runtime, the firewall and the setup
airlock build | update       build the image / rebuild it from scratch
airlock init [path]          write this project's .airlock.toml from what it uses
airlock config init|show|path|edit   your user config (init is per project, config init is yours)
airlock remote push|pull|open <host> [path]   move the project to a server with git (see below)
airlock --remote <ssh-host> <command>         run a command on a server over ssh
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
  (`credentials.scope = "shared"`) or per project. `airlock rm` keeps the volume, so a
  recreated sandbox stays logged in; `airlock rm --purge` deletes a per-project one.

## Worktrees: parallel work and Gerrit reviews

Work on several branches at once, one git worktree each, and review Gerrit patches
next to your own feature. By default every worktree shows up as a **Herdr workspace in
the same session**, so you switch between them without opening terminals.

```sh
airlock wt add rate-limit "rate limiting of the API, only includes/api/"
airlock review 12345                       # a Gerrit change, a GitHub pull request or a GitLab merge request
airlock review https://github.com/acme/widgets/pull/321     # also GitLab merge requests and Bitbucket pull requests
airlock review https://gerrit.example.org/r/c/core/+/12345/3 "check the SQL"
airlock wt                                 # worktrees, branches, changes, sandboxes, focus
airlock wt open rate-limit                 # come back to one
airlock wt focus rate-limit "now: the tests"   # change what its agent concentrates on
airlock wt handoff rate-limit "start at the retry path"   # a new agent takes over, with a briefing
airlock wt rm review-12345                 # remove the worktree, keep the branch
airlock wt prune                           # list worktrees whose folder you deleted by hand (--force forgets them)
```

- `add` creates the worktree under `.worktrees/` (git-excluded) on a new branch from
  `HEAD` (`-b` to name it or use an existing or remote branch, `--from` to start
  elsewhere). `review` takes a Gerrit change, a GitHub pull request or a GitLab merge request. It fetches
  the ref the server publishes for it (`refs/changes/NN/N/P`, `refs/pull/N/head`,
  `refs/merge-requests/N/head`, `refs/pull-requests/N/from`) **on the host**, where your git credentials are: it needs no
  API and no token, only a remote that git can already reach. It checks the result out on a
  **local branch** (`review/12345`, `review/pr-321`, `review/mr-45`, `review/bb-7`), so a review can include
  changes of your own. Run it again and the branch moves to the newest patchset or head,
  unless that would lose uncommitted changes or local commits: then it says so and prints
  the `git rebase --onto` to run.
- What you can give it: the **URL** of the change (it picks the remote that points at that
  repository, so a pull request against `upstream` works from a fork); a **number** (`12345`,
  or `12345/3` for a Gerrit patchset), which airlock looks up on `gerrit`, `origin` and
  `upstream` in turn, asking each which kind of change that number is; or `pr:12`, `mr:12`,
  `bb:7`, `gerrit:12345` when a number exists as more than one kind. Gitea and Forgejo pull
  requests work like GitHub's. Bitbucket means Server / Data Center; Bitbucket Cloud does not
  publish pull requests as git refs, so airlock says so and points to `wt add -b <branch>`. `git.review_remote` pins the remote (`git.gerrit_remote` does the
  same for Gerrit only).
- Both start the sandbox and attach. If another terminal already has Herdr open on
  that sandbox, they stay quiet instead: the new workspace appears there, without taking
  its focus, and nothing attaches (`airlock wt open <name>` brings you to it). `--attach`
  attaches anyway, `--no-attach` never does. airlock knows because the `airlock` process
  holding that terminal leaves a small file in its cache folder, which dies with the
  terminal; a Herdr left behind inside the sandbox by a closed window does not count.
- **A task per worktree.** Text after the name or change is the worktree's *focus*.
  A review also gets a task of its own, written from the fetched commit: what is under
  review, the commit (by hash, so it holds even if the branch could not be moved), the
  subject and the author, with the rules of a review (no push or merge, no edits unless asked,
  findings by file) and a pointer to the forge's tools for the description and comments.
  A pull or merge request can hold several commits and its target branch only exists on the
  forge: the task gives a likely base (the merge-base with the remote's default branch) and
  tells the agent to check it.
  The agent reads both every time it starts in the worktree, so every new session there has
  its scope, and `wt focus` applies to the next one. They are files of the host, in a folder
  of airlock's state (`<cache>/airlock/tasks/<project>/<worktree>/`), mounted read-only into the
  sandbox at `/run/airlock/tasks/<worktree>/`: they never show in `git status`, and the agent
  cannot change them. `wt rm` deletes the folder; `wt prune` (for a worktree deleted by hand)
  leaves it behind in the state folder. Task files from before this version, in the worktree's
  git directory, are not read: run `airlock wt focus <name> "..."` again to set the focus.

### Handing a worktree to a new agent: `wt handoff`

When an agent is stuck, has run out of context, or another model would do better, hand
the worktree over. The new agent (the same kind, or `--agent claude` / `--agent pi`)
starts fresh with a briefing instead of from nothing:

```sh
airlock wt handoff rate-limit "the retry path still fails: start there"
airlock wt handoff rate-limit --agent claude
airlock wt handoff rate-limit --reset        # delete the briefing, do nothing else
```

- It refuses while the previous agent is still working, and when the sandbox's agent list
  cannot be read (`--force` overrides both). The previous
  agent is the one Herdr lists in the worktree's folder; none there is fine.
- The briefing is `handoff.md` in the worktree's task folder, next to the focus and the review
  (see above), so it never shows in `git status`. It opens with airlock's instruction to check the state
  with git before changing anything, then your note, the transcript path of the previous
  conversation (inside the sandbox, read if its reasoning is needed), and what `airlock summary`
  says the worktree changed. Everything from the worktree is marked as data, not instructions.
- The worktree's Herdr workspace is closed, the previous agent is confirmed gone, then the
  briefing is written and a new agent starts in the worktree with `--fresh`.
  The agent gets the briefing in its system prompt. An agent that is resumed later with
  `--continue` gets it too: the briefing is appended on every start, like the focus, until it is
  reset or replaced. A conversation continued later reads the same briefing again.
- The briefing stays until the next handoff overwrites it, `airlock wt handoff <name> --reset`
  deletes it, or the worktree is removed. Until then every agent started in the worktree gets it,
  so run `--reset` when the handoff is over.
- It needs the Herdr UI (`sandbox.ui = "herdr"`).
- Only one agent runs in a folder: while the new agent is in the worktree, `airlock wt open` goes
  to it, whatever its kind. `--agent` applies to this handoff only. Once that agent is closed,
  `wt open` starts the configured kind again, unless you pass `--agent` again.
- With `--agent` a different kind than the configured one, airlock may print "the configuration
  changed since ... run `airlock restart`" when it brings the sandbox up. For the agent kind alone
  that is harmless: do not restart the sandbox for it.
- Not tried end to end: there was no Docker to run the sandbox and Herdr in, so the listing
  of agents, the closing and the fresh start in a real workspace have not run. The briefing,
  the choice of previous agent, the refusal, the check after the close, the file handling, the wrappers and the one-agent-per-folder rule of `airlock-herdr-up` (against a stub Herdr) are tested.

### Before you merge: `airlock summary`

```sh
airlock summary rate-limit      # commits past the base, files, uncommitted and untracked, focus
airlock summary                 # the same for the worktree or project you are in
airlock summary --json          # the same data for scripts
```

- The base is the branch's upstream if it has one, else the remote's default branch
  (`origin/HEAD`), else the first of `origin/main`, `origin/master`, `main`, `master` that
  exists. A review branch has no upstream, so it uses the default branch. A pushed feature
  branch whose upstream is its own copy on the remote (`git push -u origin feature`) is not
  used as the base: the default branch is. `git push -u origin feature:other-name` is not
  detected, so its base is that branch's own tip and only the uncommitted changes show.
- Files are what `git diff` shows against the merge-base: committed and uncommitted changes
  to tracked files, with added and deleted lines. Binary files are marked, not counted.
  Untracked files are listed (the first 50), not read.
- It does not show the content of the changes (`git diff <base>` does), whether the tests
  pass, or what the agent said. The audit line is project-wide, not about one worktree; it
  is read without recording a baseline, so `airlock audit --accept` is still what
  acknowledges changes.
- Everything it prints from the worktree (focus, commit subjects, names) is escaped, so an
  escape sequence or a bidi character in them does not reach your terminal.

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
lives in `.worktrees/.isolated/`, mounts only itself, the repository's `.git`
(hooks, config and its own `config.worktree` read-only) and its own task folder (read-only), and has its own terminal and
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

### Inbox: what your agents are waiting for

With several worktrees running agents at once, an agent that stopped on a question
waits until you look. `airlock inbox` lists the agents of every running sandbox, with
their worktree and status, blocked ones first:

```
PROJECT  WORKTREE       AGENT           KIND  STATUS
app      review-pr-7    pi-review-pr-7  pi    blocked
app      -              pi              pi    idle
```

`airlock inbox --watch` polls every 5 seconds and prints each status change with its time.
It notifies you when an agent is blocked or finishes (goes from working to done or idle).
An agent that is already blocked when the watch starts, or that appears blocked, is notified
too. The agents that need you in one poll make one notification: "pi-review-7 needs you",
or "3 agents need you". Ctrl-C stops it. A notification always rings the terminal bell;
it also shows a desktop notification (`notify-send` on Linux, `osascript` on macOS). To send
it somewhere else, set your own command in the user config. airlock appends the title and
the message to its arguments, with no shell in between:

```toml
[notify]
command = ["sh", "-c", "curl -s -d \"$2\" ntfy.sh/my-topic", "notify"]   # ntfy.sh, to your phone
```

Only your user config can set `[notify]`: a project's `.airlock.toml` cannot, because the
command runs on your computer. With `airlock --remote host inbox --watch` the watch and its
notifications run on the remote machine, so they reach that machine's desktop or ntfy topic.

Each sandbox is asked with `herdr agent list`, as the session user, under `timeout` so a
hung Herdr is killed. A sandbox without a Herdr server simply has no agents. A sandbox that
times out, answers with something that is not an agent list, or sends more than 1 MiB is
reported on stderr in one-shot mode (`airlock: warning: <sandbox> did not answer`), and in
watch mode its agents are kept from the last poll. A sandbox is taken into account for at
most 50 agents.

Everything an agent reports (its name, directory, status) comes from inside the sandbox.
airlock removes control characters, terminal escape sequences and Unicode format
characters from it, and caps its length, before it reaches your terminal or a
notification. That keeps the text from moving the cursor, rewriting the screen or hiding
other text. It does not make the text trustworthy: a name can still say anything. For
notify-send, `&`, `<` and `>` are escaped, because many notification daemons render markup.
Other notifiers receive the text as it is, with the characters above removed.

`--json` prints the same rows as an array. The parsing, classification, sorting, change
detection and sanitizing are unit tested, and the query path is tested with a fake backend.
Not tested end to end: the desktop notifications on macOS and notify-send, and the watch
against a real Herdr with agents changing state.

Known limitation: on Windows the project's path is a host path, and the worktree column
does not map it to the sandbox path, so worktrees show as `-`.
## Remote servers: keep the agents running while the laptop sleeps

`airlock --remote <host> <command>` runs airlock on a server over ssh, and Herdr
keeps the agents alive there. `airlock remote` moves the project itself, with git:

```
airlock remote push <host> [path]   send the committed branch to the server
airlock remote open <host> [path]   start or attach the sandbox there
airlock remote pull <host> [path]   fetch the server's branches, for review
```

- **push** needs a branch and a clean tree: commit or stash first, because only
  commits travel. `-b <branch>` pushes another local branch (names starting with `-`
  or `+` are refused). `--force` does not mean a git force push: it allows the push
  with uncommitted changes on this machine, which stay here. Pushing never rewrites
  history on the server; a server branch with commits you lack makes git refuse, and
  you pull first. It checks that `airlock` is installed on the server
  (`curl -fsSL https://raw.githubusercontent.com/alexghirelli/airlock/main/install.sh | sh`
  installs it there), refuses a server checkout with uncommitted changes, creates
  `airlock-remote/<project>` in the server's home directory, adds a git remote
  `airlock-<host>-<project>` to your repository, and switches the server's checkout to
  the pushed branch. If that last switch fails, the branch is pushed but the server
  stays on its old branch: fix it and rerun.
- **open** is `airlock --remote <host> airlock-remote/<project>`. The path is
  relative to the server's home directory, where ssh starts a command, so it is the
  folder push wrote to. The sandbox runs with the **server's** configuration: your
  local mounts, environment variables and MCP servers do not exist there.
- **pull** fetches into `airlock-<host>-<project>/*` (pruning branches the server
  deleted) and prints the branches that moved, with the commands to look at them
  (`git log --oneline <branch>..<remote>/<branch>`) and to merge them
  (`git merge <remote>/<branch>`). It never merges. Files the agent changed on the
  server but did not commit do not travel: commit them inside the sandbox there
  (`airlock --remote <host> shell airlock-remote/<project>`), then pull again.
  Untracked files do not travel either.
- Each project has its own folder and remote, so a worktree and its project are
  separate on the server. Moving or renaming the project folder changes its name,
  and the next push creates a new copy on the server; the old copy stays until you
  delete it there.
- Every ssh call opens a connection. For speed, an ssh `ControlMaster` setting in
  `~/.ssh/config` for the host keeps one connection open for them all.

Security notes:

- `push` sends the committed history of the branch to the server, and the server
  keeps it. Secrets from your environment and mounts are not copied, but a secret
  committed to the repository is.
- The server never needs access to your computer: the traffic is ssh and git.
- What the agent wrote on the server is untrusted until you have reviewed it. Review
  a pulled branch before you merge it, and run `airlock audit` after merging, before
  you run the code on your computer.

Tests use a fake ssh and git between two local folders (`AIRLOCK_SSH` names the ssh
client, a hook for tests and unusual setups). This has not been run against a real
server.

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
packages = ["npm:pi-claude-bridge@0.11.0"]   # needs Claude Code, which airlock then installs too
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

### Starting a project: airlock init

`airlock init` looks at the project root and writes `.airlock.toml` with the packages
the project needs (`extras.mise`, `extras.apt`), each with a comment saying which file
it came from. Toolchains that a version file already pins (`.nvmrc`, `.python-version`,
`.tool-versions`, `mise.toml` and the like) are left alone. It detects Node, Go, Python,
Ruby, Rust and PHP (PHP comes from apt: mise would compile it for minutes). It writes at
the repository root, also when you run it inside a worktree. It refuses to overwrite an
existing file, and never writes through a symlink, even with `--force`; `--json` only
prints the result. The project is untrusted, so what init prints is cleaned of control
characters, escape sequences and bidi overrides, and a secret-looking file whose name
holds a line break or such a character is counted, not listed.

Services are matched by name: an image whose last path segment contains `mariadb`,
`mysql`, `postgres`, `postgis`, `redis`, `valkey`, `memcached`, `mongo`, `elasticsearch`,
`opensearch` or `rabbitmq` is listed (so `bitnami/postgresql` counts, and so can an
exporter with such a name).

It does not write your user config. It prints what belongs there instead: MCP config
files to pass to `airlock mcp import`, compose services that run a database (airlock has
no service containers yet), and secret-looking files to add to `.airlockignore`.
`airlock config init` is the other command: it writes your user config.

### Where your additions go

The published image is only the base. What you add never changes it, and is kept on
your machine, in the cheapest place that fits:

| You add | Where it lives | Cost |
|---|---|---|
| `extras.mise`, `[[setup]]`, Pi packages, logins | Docker volumes, filled when the sandbox starts | nothing to rebuild |
| `extras.apt` | a layer built on top of the downloaded image | seconds, rebuilt when the list changes |
| an agent other than Pi, or another Pi version | the tools volume, installed on first use (see [Agents](#agents-use-the-one-you-prefer)) | seconds to a minute, once per version |
| `[image] dockerfile`, project `extras.apt` | a per-project image built on top of the base | seconds, rebuilt when the files change |

A new airlock release publishes a new image; the first run after upgrading
downloads it, and layers you had added are rebuilt on top. The old image is removed
once nothing uses it. `airlock update` rebuilds from scratch locally instead of
downloading.

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

### Cleaning up

`airlock gc` lists what is left behind, and `airlock gc --force` removes it:

- sandboxes whose project folder no longer exists (a deleted project or worktree);
- the credentials volume of such a project (`credentials.scope = "per-project"`;
  volumes made before this version are not labelled and are never touched, remove
  those with `airlock rm --purge` in their project);
- `airlock:` images of older releases that no sandbox uses.

The shared logins, the tools volume, your project files and anything that is still
in use are never collected. Without `--force` it only lists. `airlock wt prune`
does the same for worktrees whose folder you deleted by hand (their branches stay).

To stop sandboxes nobody uses, set `idle_stop` in your user config:

```toml
[sandbox]
idle_stop = "2h"                 # at least 5m; empty = never (the default)
```

A sandbox stops after that long with no terminal attached and no agent working.
`airlock` starts it again, and the agents continue their conversations (see
[Agents](#agents-use-the-one-you-prefer)). Anything else running in it stops too,
a dev server for one. The setting applies the next time the sandbox starts
(`airlock restart`).

### Example setups

1. **Anthropic API key**: `default_model = "anthropic/<id>"`, run `airlock login`.
2. **Claude subscription**: `packages = ["npm:pi-claude-bridge@0.11.0"]`,
   `default_model = "claude-bridge/<id>"`, run `airlock login` (it runs
   `claude auth login`; the bridge uses Claude Code's login, not Pi's).
3. **API key from the environment**: add it under `[env]` and export it on the host.

## Agents: use the one you prefer

Pi is the default and comes in the image. The agent is a setting, so you can run
another one, at the version you want:

```toml
[agent]
kind = "claude"                  # pi (default) | claude
version = "pinned"               # pinned (tested with this airlock) | latest | "2.1.322"
args = ["--dangerously-skip-permissions"]   # extra arguments; the sandbox is the boundary
```

`airlock --agent claude` does the same for one run, and `airlock review 123 --agent
claude` for one worktree, so a Pi session and a Claude Code one can work side by
side in the same Herdr. The first use installs the agent into the tools volume, a
folder per version, so projects share the download and can use different versions;
Claude Code comes from Anthropic's own installer and Pi from npm. Nothing is baked
into the image, which also keeps it free of anything that cannot be redistributed.
Then run `airlock login` once.

**Closing a workspace does not lose the conversation.** Pi and Claude Code keep their
sessions in the credentials volume, one per folder. When a workspace is opened again
(`airlock wt open`, a worktree that had been closed, a sandbox that was stopped or
recreated), the agent starts with `--continue` and picks up the last conversation of
that folder; a folder without one starts fresh. `--fresh` starts a new conversation
for one run (`airlock wt open rate-limit --fresh`) and `agent.resume = false` turns
the continuing off. A worktree has its own conversation, separate from the project's
and from every other worktree's, and a team (`--team`) always starts fresh.

What airlock does for you depends on the agent:

| | Pi | Claude Code |
|---|---|---|
| Runs in a sandbox with the firewall, worktrees and Herdr workspaces | yes | yes |
| Sandbox instructions (where you are, mounts, MCP servers) | yes | yes, added to its system prompt |
| MCP servers from `[[mcp]]` | yes | yes |
| Task of a worktree (`airlock review`, `wt focus`) | yes | yes |
| Skills from mounts, the `concise` skill, `pi.default_model`, packages | yes | no, these are Pi's |
| `--team` | yes | no |

Inside the sandbox airlock marks the project as trusted for Claude Code (the project
is the reason the sandbox exists) and skips its first-run screens. Other agents are
not wired up yet; a `kind` is a small adapter, and adding one is a good first
contribution.

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
- The task files of a worktree (focus, review, handoff briefing) are host files:
  they live in airlock's state folder, which the sandbox never sees. They are mounted
  read-only: an isolated worktree's sandbox sees only its own folder, and the project's
  sandbox sees the folders of the worktrees that share it (as it sees the worktrees themselves). The agent cannot change what the
  host reads (a link or a FIFO in the worktree's git directory is ignored, and the
  reads are capped at 64 KiB), nor what the next agent is told at its start.
  An agent can still influence what it later reads from the project and from commit
  messages, since the briefings call them data, not instructions: that boundary is
  advisory, not enforced.
- No git credentials in the box by default. `git.mode = "push"` injects one token
  from a host variable, offered only to the configured hosts. `~/.ssh` is never
  mounted.
- `airlock audit` reports changes to hooks, `.envrc`, `.vscode/`, build and CI
  files, `package.json`/`composer.json` scripts and new executables. It reports; it
  does not revert. The baseline moves only when you run `airlock audit --accept`.

What it does **not** protect against:

- **Data leaving over the internet.** The default network mode lets the agent reach
  the internet, so a prompt-injected agent can send out anything it can read: your
  source, the environment variables you pass in, the tokens you mount. The firewall
  blocks your computer and LAN, not the outside. Keep secrets out of the project
  (`.airlockignore` hides files from the box; `airlock doctor` warns about visible
  secret-looking ones) and out of the variables you pass. A domain allowlist is
  not built yet.
- **A container shares the host kernel.** The agent is root inside the box (it has
  `sudo`, so it can install packages), and a kernel exploit could escape from there.
  Docker Sandboxes and similar tools use a microVM for this; airlock has a gVisor
  option (`runsc`, not yet part of the test suite) and a microVM backend is only an
  idea so far.
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
  resolution), so DNS can carry data out.
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
| `airlock remote` | Tested with a fake ssh and two local folders; never run against a real server. The server-side scripts run under `sh -c`, so the login shell can be anything that runs `sh` and `airlock`. |
| Windows | The release archive compiles for it, but airlock has not been run there (WSL2 is the way to go, see `airlock doctor`). |

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
`.airlockignore`, `audit`, git modes and `doctor`, and Claude Code and other Pi
versions as the agent. The test that installs Pi packages together with Claude
Code is opt-in (`AIRLOCK_IT_SLOW=1`).

CI runs only the fast checks on every push (formatting, vet, unit tests,
cross-compilation). The integration tests run weekly, and on demand from the
Actions tab (the `integration` workflow).

The tests set `AIRLOCK_PRUNE_IMAGES=0` so the several image variants they build
survive each other. Normally airlock removes superseded `airlock:*` tags after a
build; the image is about 1.3 GB.

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

The image downloads Pi (MIT), Herdr (Apache-2.0) and mise at build time; each keeps
its own licence and terms. Claude Code is proprietary and is not in the image: when
you choose it, Anthropic's installer fetches it onto your machine, under Anthropic's
terms.
