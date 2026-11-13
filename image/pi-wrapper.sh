#!/bin/bash
# Runs Pi on its private Node, so project toolchains can never shadow it, and
# prepends this sandbox's defaults (skills from mounts, default model) from
# /etc/airlock/pi.args, and the task of the worktree it starts in. They live outside the shared credentials volume on
# purpose: they differ per project.
# The folders of the task files, one per worktree, mounted read-only by airlock.
tasks_root=/run/airlock/tasks
# The task folder of the linked worktree the current directory is in. Only a
# linked worktree has one: its .git is a file, and it is named after its folder,
# as airlock names worktrees.
task_dir() {
  local top
  top="$(git rev-parse --show-toplevel 2>/dev/null)" || return 1
  [[ -f "$top/.git" ]] || return 1
  printf '%s/%s\n' "$tasks_root" "${top##*/}"
}
args=()
case "${1:-}" in
  install|remove|uninstall|update|list|config|auth|mcp|-h|--help|-v|--version|--list-models|--export) ;;
  *) [[ -r /etc/airlock/pi.args ]] && mapfile -t args </etc/airlock/pi.args
     # The task of the git worktree Pi starts in (airlock review, wt focus, wt handoff).
     # The sandbox cannot write its folder, so the agent cannot change what it reads.
     if task="$(task_dir)"; then
       # The handoff briefing stays until it is reset or replaced, like the focus:
       # a resumed conversation gets it too.
       for f in review.md focus.md handoff.md; do
         [[ -r "$task/$f" ]] && args+=(--append-system-prompt "$task/$f")
       done
     fi ;;
esac
# Test hook: show the arguments Pi would get, without starting it.
if [[ -n "${AIRLOCK_PI_ARGS_ONLY:-}" ]]; then printf '%s\n' "${args[@]}" "$@"; exit 0; fi
# The image has one Pi version; another one (agent.version) is installed into the
# tools volume by airlock-agent-install.
bin=/opt/pi/bin/pi
ver="$(jq -r '.agent.versions.pi // empty' /etc/airlock/manifest.json 2>/dev/null)"
if [[ -n "$ver" && "$ver" != "$(cat /opt/pi/VERSION)" ]]; then
  alt="$HOME/.local/share/mise/agents/pi/$ver/bin/pi"
  if [[ ! -e "$alt" ]]; then airlock-agent-install pi >&2 || echo "airlock: could not install Pi $ver; using the image's" >&2; fi
  [[ -e "$alt" ]] && bin="$alt"
fi
exec /opt/pi/node "$(readlink -f "$bin")" "${args[@]}" "$@"
