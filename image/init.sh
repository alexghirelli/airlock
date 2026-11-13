#!/usr/bin/env bash
# Runs as the unprivileged `agent` at every container start, then execs the
# container command. Everything user-configurable arrives in
# /etc/airlock/manifest.json (written by the airlock CLI before each start);
# the global agent instructions arrive as /etc/airlock/AGENTS.md.
set -euo pipefail

export HOME=/home/agent USER=agent
MANIFEST=/etc/airlock/manifest.json
say()  { echo "airlock: $*" >&2; }
# Warnings are also kept in /run/airlock/warnings, so the airlock CLI can show
# them after start instead of leaving them in the container log.
WARN_FILE=/run/airlock/warnings
: >"$WARN_FILE" 2>/dev/null || true
warn() { echo "airlock: warning: $*" | tee -a "$WARN_FILE" >&2 || true; }
m()    { jq -r "$1" "$MANIFEST"; }

[[ -r "$MANIFEST" ]] || { say "missing $MANIFEST"; exit 1; }

# --- credentials volume: everything a harness writes under $HOME that must persist
AGENT_DIR="${PI_CODING_AGENT_DIR:-/creds/pi-agent}"
mkdir -p "$AGENT_DIR" "${CLAUDE_CONFIG_DIR:-/creds/claude}" "$HOME/.pi" "$HOME/.local/bin" "$HOME/.local/share/mise"
# Tools that hard-code ~/.pi/agent (Herdr's integration) end up in the volume too.
if [[ ! -L "$HOME/.pi/agent" ]]; then rm -rf "$HOME/.pi/agent"; ln -s "$AGENT_DIR" "$HOME/.pi/agent"; fi

# --- older airlock versions wrote the instructions into the shared volume, where
# concurrent sandboxes would overwrite each other's; drop that copy.
if [[ -f "$AGENT_DIR/AGENTS.md" ]] && head -n 1 "$AGENT_DIR/AGENTS.md" | grep -q 'managed by airlock'; then
  rm -f "$AGENT_DIR/AGENTS.md"
fi

# --- Pi settings overlay: the user's own settings and the npm command.
# (Per-project defaults, like skills and the default model, are not written to
# this shared file; they reach Pi as arguments through /etc/airlock/pi.args.)
settings="$AGENT_DIR/settings.json"
[[ -s "$settings" ]] || echo '{}' >"$settings"
if ! jq -s '.[0] * .[1]' "$settings" <(m '.pi.settings') >"$settings.tmp"; then
  rm -f "$settings.tmp"; warn "could not merge Pi settings; leaving settings.json unchanged"
else
  mv "$settings.tmp" "$settings"
fi

# --- MCP servers declared in the airlock config. The names we manage are
# recorded, so servers removed from the config disappear here too and servers
# added by hand are left alone.
mcp="$AGENT_DIR/mcp.json"
managed="$AGENT_DIR/.airlock-mcp"
[[ -s "$mcp" ]] || echo '{}' >"$mcp"
old='[]'; [[ -s "$managed" ]] && old="$(cat "$managed")"
if jq --argjson old "$old" --argjson new "$(m '.pi.mcp')" \
     '.mcpServers = (((.mcpServers // {}) | with_entries(select(.key as $k | $old | index($k) | not))) + $new)' \
     "$mcp" >"$mcp.tmp"; then
  mv "$mcp.tmp" "$mcp"; m '.pi.mcp | keys' >"$managed"
else
  rm -f "$mcp.tmp"; warn "could not update mcp.json"
fi

# --- git: identity always; credentials only in push mode
git config --global --replace-all safe.directory '*'
name="$(m '.git.name // empty')"; email="$(m '.git.email // empty')"
[[ -n "$name" ]]  && git config --global user.name "$name"
[[ -n "$email" ]] && git config --global user.email "$email"
git config --global --unset-all credential.helper 2>/dev/null || true
if [[ "$(m '.git.mode')" == push ]]; then
  while read -r host; do
    [[ -n "$host" ]] || continue
    git config --global "credential.https://$host.helper" \
      '!f() { test "$1" = get && echo username=x-access-token && echo "password=$AIRLOCK_GIT_TOKEN"; }; f'
  done < <(m '.git.hosts[]?')
fi

# --- Herdr's first-run screens (a welcome box, then a page offering agent integrations)
# would greet every new sandbox; airlock installs the integrations it needs itself. A
# config that is already there (yours, mounted in) is left as it is.
herdr_cfg="$HOME/.config/herdr/config.toml"
if [[ ! -e "$herdr_cfg" ]]; then
  mkdir -p "$(dirname "$herdr_cfg")"
  echo 'onboarding = false' >"$herdr_cfg"
fi

# --- Herdr <-> Pi state reporting (idempotent)
herdr integration install pi >/dev/null 2>&1 || warn "'herdr integration install pi' failed"

# --- Pi packages: installed into the credentials volume, so reinstall only
# what is missing from settings.json (pinned versions in the spec stay pinned).
while read -r pkg; do
  [[ -n "$pkg" ]] || continue
  if ! jq -e --arg s "$pkg" '(.packages // []) | map(if type == "object" then .source else . end) | index($s)' "$settings" >/dev/null; then
    say "installing Pi package $pkg"
    pi install "$pkg" >&2 || warn "could not install $pkg (is the network policy blocking it?)"
  fi
done < <(m '.pi.packages[]?')

# --- agents: the one this sandbox opens (and Claude Code, when a Pi package goes
# through it) are installed now, into the tools volume, so the first attach does
# not have to wait. Pi at the image's version is already there.
while read -r kind; do
  [[ -n "$kind" ]] || continue
  airlock-agent-install "$kind" || warn "could not install $kind (is the network policy blocking it?)"
done < <(m '.agent.install[]?')

# --- toolchains requested in the config (idempotent; cached in the tools volume)
while read -r tool; do
  [[ -n "$tool" ]] || continue
  mise use -g "$tool" >&2 || warn "could not install $tool with mise"
done < <(m '.mise[]?')

# --- setup steps from the user config. A step is done when the hash of its
# command is recorded and its `creates` path exists; a failed step is retried
# at the next start and does not stop the sandbox.
setup_state="$HOME/.local/share/mise/.airlock-setup"
mkdir -p "$setup_state"
while read -r step; do
  [[ -n "$step" ]] || continue
  name="$(jq -r .name <<<"$step")"; run="$(jq -r .run <<<"$step")"
  creates="$(jq -r '.creates // empty' <<<"$step")"; creates="${creates/#\~/$HOME}"
  # Paths to watch: their file list with sizes and mtimes goes into the hash.
  watched=""
  while IFS= read -r wp; do
    [[ -n "$wp" ]] || continue
    wp="${wp/#\~/$HOME}"
    watched+="$(find "$wp" \( -name .git -o -name __pycache__ -o -name node_modules -o -name '*.log' \) -prune -o -type f -printf '%P %s %T@\n' 2>/dev/null | sort | sha256sum)"
  done < <(jq -r '.watch[]? // empty' <<<"$step")
  sum="$(printf '%s%s' "$run" "$watched" | sha256sum | cut -d' ' -f1)"
  if [[ "$(cat "$setup_state/$name" 2>/dev/null || true)" == "$sum" && ( -z "$creates" || -e "$creates" ) ]]; then
    continue
  fi
  say "setup: $name"
  if (cd "$HOME" && bash -c "$run") </dev/null >&2; then
    echo "$sum" >"$setup_state/$name"
  else
    rm -f "$setup_state/$name"
    warn "setup step '$name' failed (it runs again at the next start)"
  fi
done < <(m '.setup[]? | @json')

# --- stop the sandbox when nobody uses it (sandbox.idle_stop). The exec below keeps
# this shell's pid for the container's main process, which is what the watcher ends.
idle="$(m '.idle_stop_seconds // 0')"
if (( idle > 0 )); then
  echo $$ >/run/airlock/main-pid
  setsid airlock-idle-watch "$idle" </dev/null >/dev/null 2>&1 &
fi

touch /run/airlock/ready
exec "$@"
