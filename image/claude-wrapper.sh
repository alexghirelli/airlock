#!/bin/bash
# Claude Code, installed on first use (airlock-agent-install) and started with this
# sandbox's instructions, MCP servers and the task of the worktree it starts in,
# the way pi-wrapper.sh does for Pi. Claude Code reads CLAUDE.md, not AGENTS.md, so
# the generated instructions are appended to its system prompt.
MANIFEST=/etc/airlock/manifest.json
ver="$(jq -r '.agent.versions.claude // empty' "$MANIFEST" 2>/dev/null)"
bin="$HOME/.local/share/mise/agents/claude/$ver/home/.local/bin/claude"
if [[ ! -x "$(readlink -f "$bin")" && -z "${AIRLOCK_AGENT_ARGS_ONLY:-}" ]]; then
  airlock-agent-install claude || { echo "airlock: Claude Code is not installed (is the network blocking claude.ai?)" >&2; exit 1; }
fi
args=()
case "${1:-}" in
  auth|mcp|doctor|install|update|config|plugin|setup-token|-h|--help|-v|--version) ;;
  *)
    mapfile -t args < <(jq -r '.agent.args[]? // empty' "$MANIFEST" 2>/dev/null)
    [[ -r /etc/airlock/claude-mcp.json ]] && args+=(--mcp-config /etc/airlock/claude-mcp.json)
    prompt=""
    [[ -r /etc/airlock/AGENTS.md ]] && prompt="$(cat /etc/airlock/AGENTS.md)"
    # The task of the git worktree it starts in (airlock review / wt focus), kept in
    # the worktree's own git directory so it stays out of `git status`.
    if gd="$(git rev-parse --absolute-git-dir 2>/dev/null)"; then
      for f in airlock-review.md airlock-focus.md; do
        [[ -r "$gd/$f" ]] && prompt+=$'\n\n'"$(cat "$gd/$f")"
      done
    fi
    [[ -z "$prompt" ]] || args+=(--append-system-prompt "$prompt")
    ;;
esac
# Test hook: show the arguments Claude Code would get, without starting it.
if [[ -n "${AIRLOCK_AGENT_ARGS_ONLY:-}" ]]; then printf '%s\n' "${args[@]}" "$@"; exit 0; fi
# Through the link, not the file it points to: the real file is named after the
# version, and Herdr recognises the agent by the process name "claude".
exec "$bin" "${args[@]}" "$@"
