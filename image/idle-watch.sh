#!/bin/bash
# Stops the sandbox after it has been idle for a while (sandbox.idle_stop):
#   airlock-idle-watch SECONDS
# Started by init.sh in the background. The sandbox is in use while
#   - something runs that was started from outside with `docker exec`: a terminal
#     (`airlock`, `airlock shell`) or a command; such a process has no parent here
#   - an agent is working (Herdr says so)
# Only a whole period of neither stops it, by ending the container's main process.
# The agents keep their conversations in the credentials volume, so starting the
# sandbox again and reopening a workspace continues where it stopped.
# Anything else running at that moment, a dev server for one, stops with it.
limit="${1:?usage: airlock-idle-watch SECONDS}"
step="${AIRLOCK_IDLE_STEP:-30}"                          # a test turns the clock up
pidfile="${AIRLOCK_IDLE_MAIN_PID_FILE:-/run/airlock/main-pid}"
main="$(cat "$pidfile")" || exit 1

in_use() {
  # An exec'd process has no parent inside the container (ppid 0); so has init, pid 1.
  ps -eo ppid=,pid=,comm= | awk -v me="$$" '$1 == 0 && $2 != 1 && $2 != me { found = 1 } END { exit !found }' && return 0
  herdr agent list 2>/dev/null | jq -e '[.result.agents[]? | select(.agent_status == "working")] | length > 0' >/dev/null 2>&1
}

idle=0
while kill -0 "$main" 2>/dev/null; do
  sleep "$step"
  if in_use; then idle=0; else idle=$((idle + step)); fi
  if (( idle >= limit )); then
    echo "airlock: idle for $((idle / 60)) minutes; stopping the sandbox (sandbox.idle_stop)" >&2
    kill "$main"
    exit 0
  fi
done
