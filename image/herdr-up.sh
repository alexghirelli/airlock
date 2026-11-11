#!/bin/bash
# Makes sure the project's Herdr session has its agent running, before the UI
# client attaches. Idempotent: attaching again later finds everything in place.
#   airlock-herdr-up                  a workspace for the project with Pi running in it
#   airlock-herdr-up --team           the same, with Pi turned into the team lead
#   airlock-herdr-up --workspace DIR  the same for a git worktree DIR that shares this
#                                     sandbox ([--no-focus] leaves the view where it is)
#   airlock-herdr-up --close DIR      close the workspace of a worktree that is going away
set -euo pipefail

project="${AIRLOCK_PROJECT_DIR:-$PWD}"
dir="$project"; team=0; focus=--focus; close=0
while (( $# )); do
  case "$1" in
    --team) team=1 ;;
    --no-focus) focus=--no-focus ;;
    --workspace) dir="$2"; shift ;;
    --close) dir="$2"; close=1; shift ;;
  esac
  shift
done
label="$(basename "$dir")"
agent=pi
if [[ "$dir" != "$project" ]]; then
  team=0
  agent="pi-${label//[^A-Za-z0-9_-]/-}" # one agent name per workspace
fi

ready() { herdr status server 2>&1 | grep -q 'status: running'; }

workspaces() { herdr workspace list 2>/dev/null | jq -r --arg l "$label" '.result.workspaces[] | select(.label == $l) | .workspace_id'; }

if (( close )); then
  ready || exit 0
  while read -r ws; do [[ -n "$ws" ]] && herdr workspace close "$ws" >/dev/null; done < <(workspaces)
  exit 0
fi

if ! ready; then
  # The server outlives this script and every client: that is what makes
  # detach/reattach and "close the terminal" safe.
  setsid nohup herdr server >/tmp/herdr-server.log 2>&1 </dev/null &
  for _ in $(seq 100); do ready && break; sleep 0.1; done
  ready || { echo "airlock: the Herdr server did not start:" >&2; cat /tmp/herdr-server.log >&2; exit 1; }
fi

# Already set up (a previous attach, or the agent is still running)?
lead_file=/tmp/airlock-team-lead-pane
if (( team )); then
  if [[ -s "$lead_file" ]] && herdr pane get "$(cat "$lead_file")" >/dev/null 2>&1; then exit 0; fi
else
  if herdr agent get "$agent" >/dev/null 2>&1; then
    # A worktree you open again comes to the front; the project's own workspace stays as it was.
    if [[ "$dir" != "$project" && "$focus" == --focus ]]; then
      read -r ws < <(workspaces) || true
      [[ -z "${ws:-}" ]] || herdr workspace focus "$ws" >/dev/null 2>&1 || true
    fi
    exit 0
  fi
fi

# Herdr restores saved workspaces after a server restart (a stopped and started
# sandbox), with plain shells in them. Reuse such an idle shell pane of this
# workspace instead of piling up a duplicate workspace on every start.
# A pane is an idle shell when its foreground process is the shell itself.
reusable_pane() {
  local ws pane info
  while read -r ws; do
    while read -r pane; do
      info="$(herdr pane process-info --pane "$pane" 2>/dev/null)" || continue
      if jq -e '.result.process_info | .foreground_process_group_id == .shell_pid' <<<"$info" >/dev/null; then
        echo "$pane"; return 0
      fi
    done < <(herdr pane list 2>/dev/null | jq -r --arg ws "$ws" '.result.panes[] | select(.workspace_id == $ws and .agent_status == "unknown") | .pane_id')
  done < <(workspaces)
  return 1
}

if ! pane="$(reusable_pane)"; then
  ws="$(herdr workspace create --cwd "$dir" --label "$label" "$focus")"
  pane="$(jq -er '.result.root_pane.pane_id' <<<"$ws")"
fi

if (( team )); then
  echo "$pane" >"$lead_file"
  # pi-team turns this pane into the lead and opens the specialists around it.
  herdr pane run "$pane" "pi-team up --focus" >/dev/null
else
  herdr agent start "$agent" --kind pi --pane "$pane" --timeout 120000 >/dev/null
fi
