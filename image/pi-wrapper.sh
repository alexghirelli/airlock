#!/bin/bash
# Runs Pi on its private Node, so project toolchains can never shadow it, and
# prepends this sandbox's defaults (skills from mounts, default model) from
# /etc/airlock/pi.args, and the task of the worktree it starts in. They live outside the shared credentials volume on
# purpose: they differ per project.
args=()
case "${1:-}" in
  install|remove|uninstall|update|list|config|auth|mcp|-h|--help|-v|--version|--list-models|--export) ;;
  *) [[ -r /etc/airlock/pi.args ]] && mapfile -t args </etc/airlock/pi.args
     # The task of the git worktree Pi starts in (airlock wt review / wt focus):
     # files in the worktree's own git directory, so they stay out of `git status`.
     if gd="$(git rev-parse --absolute-git-dir 2>/dev/null)"; then
       for f in airlock-review.md airlock-focus.md; do
         [[ -r "$gd/$f" ]] && args+=(--append-system-prompt "$gd/$f")
       done
     fi ;;
esac
# Test hook: show the arguments Pi would get, without starting it.
if [[ -n "${AIRLOCK_PI_ARGS_ONLY:-}" ]]; then printf '%s\n' "${args[@]}" "$@"; exit 0; fi
exec /opt/pi/node "$(readlink -f /opt/pi/bin/pi)" "${args[@]}" "$@"
