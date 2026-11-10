#!/usr/bin/env bash
# Runs as the unprivileged `agent` at every container start, then execs the
# container command. Everything user-configurable arrives in
# /etc/airlock/manifest.json (written by the airlock CLI before each start);
# the global agent instructions arrive as /etc/airlock/AGENTS.md.
set -euo pipefail

export HOME=/home/agent USER=agent
MANIFEST=/etc/airlock/manifest.json
say()  { echo "airlock: $*" >&2; }
warn() { echo "airlock: warning: $*" >&2; }
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

# --- toolchains requested in the config (idempotent; cached in the tools volume)
while read -r tool; do
  [[ -n "$tool" ]] || continue
  mise use -g "$tool" >&2 || warn "could not install $tool with mise"
done < <(m '.mise[]?')

touch /run/airlock/ready
exec "$@"
