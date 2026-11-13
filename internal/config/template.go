package config

// Template is the commented user config written by `airlock config init`.
// A test checks that it decodes strictly and equals the built-in defaults.
const Template = `# airlock user configuration.
# Precedence: built-in defaults < this file < <project>/.airlock.toml < CLI flags < AIRLOCK_* env vars.
# The project file is untrusted: it can only tighten limits and pick a few
# harmless options (see docs/configuration.md). Everything below is yours.

[sandbox]
lifetime = "persistent"          # persistent | ephemeral
ui = "herdr"                     # herdr | agent (the agent alone, no Herdr) | shell
backend = "container"            # container | gvisor | microvm | vm
memory = "8g"
cpus = 4
pids = 2048
worktrees_dir = ".worktrees"     # "" disables worktree support
worktree_isolation = "none"      # which "airlock wt" worktrees get a sandbox of their own:
                                 #   none (all share the project's, a Herdr workspace each) | reviews | all
idle_stop = ""                   # stop the sandbox after this long with no terminal attached and no agent working,
                                 #   e.g. "2h" (at least 5m); airlock starts it again and the agents continue their conversations

[network]
mode = "open-no-lan"             # open-no-lan | allowlist | open
allow = []                       # extra domains when mode = "allowlist"
allow_cidrs = []                 # holes in the LAN block, e.g. a local registry: ["192.168.1.50/32"]

[credentials]
scope = "shared"                 # shared (one login volume for all projects) | per-project

[git]
mode = "local"                   # local | push
token_env = ""                   # host env var holding a token, used only when mode = "push"
hosts = ["github.com"]           # hosts the token is offered to
review_remote = ""               # remote that "airlock review" fetches from (empty = the one a URL points at, else gerrit, origin, upstream)
gerrit_remote = ""               # remote that "airlock wt review" fetches from (empty = "gerrit" if it exists, else "origin")

[agent]
kind = "pi"                      # pi | claude: the agent that opens in the sandbox (airlock --agent claude, for one run)
version = "pinned"               # pinned (tested with this airlock) | latest | an exact version such as "2.1.322"
args = []                        # extra arguments for it, e.g. ["--dangerously-skip-permissions"] for Claude Code
resume = true                    # reopening a workspace (a closed worktree, a restarted sandbox) continues its last conversation; --fresh starts a new one
# Pi is in the image; Claude Code is installed by Anthropic's own installer into the
# tools volume the first time it is needed, so it is never part of the airlock image.

[pi]
version = "latest-pinned"        # or an exact version such as "1.5.2"
packages = []                    # Pi packages to install, e.g. ["npm:pi-claude-bridge@0.11.0"]
default_model = ""               # e.g. "anthropic/claude-opus-5-5"; empty = Pi's default
# settings = { quietStartup = true }   # merged into Pi's settings.json

[extras]
claude_code = "auto"             # install Claude Code too: auto (when a Pi package needs it) | true | false
apt = []                         # extra Debian packages, added in a layer on top of the image
mise = []                        # e.g. ["node@22"], preinstalled globally

# Environment variables passed from the host into the sandbox (stored in the
# container's configuration; see docs/security.md):
# [env]
# OPENCODE_API_KEY = { from_host_env = "OPENCODE_API_KEY" }
# VAULT_DIR = { value = "/mnt/vault" }          # a literal, not a secret

# Extra folders shared with the sandbox, mounted at /mnt/<name>. Zero or more.
# [[mount]]
# name = "notes"
# host = "~/notes"
# mode = "rw"                    # ro | rw
# description = "Personal knowledge base. Read its README first."
#
# [[mount]]
# name = "skills"
# host = "~/agent-skills"
# mode = "ro"
# skills = "."                   # load Pi skills from this folder inside the mount

# MCP servers, added to Pi's MCP config. A remote server has a url; header
# values name host environment variables, so secrets never touch this file.
# [[mcp]]
# name = "docs"
# url = "https://example.com/mcp"
# description = "Product documentation"
# headers = { Authorization = "DOCS_MCP_AUTH" }
#
# A local server is a command started inside the sandbox. Its code must be
# reachable there (a [[mount]] below, e.g. /mnt/tools/...) and its runtime must
# exist in the image (e.g. extras.mise = ["node@22"]). env holds literal
# values; env_from_host maps a variable to a host variable, for secrets.
# [[mcp]]
# name = "tracker"
# command = "node"
# args = ["/mnt/tools/mcp-tracker/src/index.js"]
# env = { TRACKER_USER = "alice" }
# env_from_host = { TRACKER_TOKEN = "TRACKER_TOKEN" }

# Preparation steps run inside the sandbox at start, before the agent: build a
# virtualenv, install node_modules, anything airlock knows nothing about. A step
# is skipped while its command is unchanged and the "creates" path exists. Only
# this file may define them; a project's .airlock.toml cannot.
# [[setup]]
# name = "gerrit-venv"
# run = """
# python -m venv ~/.local/share/mise/venvs/gerrit &&
# ~/.local/share/mise/venvs/gerrit/bin/pip install -r /mnt/tools/gerrit/requirements.txt
# """
# creates = "~/.local/share/mise/venvs/gerrit/bin/python"
# watch = ["/mnt/tools/gerrit"]   # also rerun when files there change (ignores .git, caches, *.log)

# agents_md_append = "~/airlock-agents-extra.md"   # appended to the generated global agent instructions

[team]
enabled = false
profiles_dir = ""                # empty = built-in profiles
default_roles = ["reviewer", "tester", "scout", "scribe"]

[team.models]                    # role -> model; empty = pi.default_model
# lead = ""
# reviewer = ""

[team.thinking]
lead = "high"
reviewer = "high"
tester = "medium"
scout = "medium"
scribe = "low"

[agents]
concise = true                   # built-in "concise replies" skill, on for every agent

[audit]
extra_patterns = []              # more files to report on, gitignore syntax
`
