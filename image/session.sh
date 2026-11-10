#!/bin/sh
# Entry point of every interactive or exec'd session started by airlock.
# Called as root by `docker exec`; becomes the unprivileged `agent` with
# NET_ADMIN and NET_RAW removed from the bounding set for good, so that neither
# the agent nor `sudo` can change the firewall installed by the entrypoint.
set -e
gid="$(id -g agent)"
exec setpriv --reuid agent --regid "$gid" --init-groups \
  --bounding-set -net_admin,-net_raw --inh-caps -all -- "$@"
