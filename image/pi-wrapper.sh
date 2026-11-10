#!/bin/bash
# Runs Pi on its private Node, so project toolchains can never shadow it, and
# prepends this sandbox's defaults (skills from mounts, default model) from
# /etc/airlock/pi.args. They live outside the shared credentials volume on
# purpose: they differ per project.
args=()
case "${1:-}" in
  install|remove|uninstall|update|list|config|auth|mcp|-h|--help|-v|--version|--list-models|--export) ;;
  *) [[ -r /etc/airlock/pi.args ]] && mapfile -t args </etc/airlock/pi.args ;;
esac
exec /opt/pi/node "$(readlink -f /opt/pi/bin/pi)" "${args[@]}" "$@"
