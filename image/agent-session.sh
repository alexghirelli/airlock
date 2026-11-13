#!/bin/bash
# Says whether an agent has a conversation to continue in a directory:
#   airlock-agent-session KIND DIR     exit 0 if there is one, 1 if not
# The agents keep their sessions in the credentials volume, one folder per working
# directory, named after its path. A layout this does not know means "no session":
# the agent then starts fresh, which is never worse than a failed --continue (Claude
# Code refuses to start when there is nothing to continue).
kind="${1:-}"; dir="${2:-}"
[[ -n "$kind" && -n "$dir" ]] || { echo "usage: airlock-agent-session KIND DIR" >&2; exit 2; }
dir="$(readlink -f "$dir" 2>/dev/null || echo "$dir")"

case "$kind" in
  pi)
    # /a/b/c -> <sessions>/--a-b-c--/
    name="--${dir#/}--"; name="${name//\//-}"
    sessions="${PI_CODING_AGENT_SESSION_DIR:-${PI_CODING_AGENT_DIR:-$HOME/.pi/agent}/sessions}/$name" ;;
  claude)
    # every character that is not a letter or digit becomes a dash
    sessions="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects/$(sed 's/[^A-Za-z0-9]/-/g' <<<"$dir")" ;;
  *) exit 1 ;;
esac
compgen -G "$sessions/*.jsonl" >/dev/null
