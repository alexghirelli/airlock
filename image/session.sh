#!/bin/sh
# Entry point of every interactive or exec'd session started by airlock.
# Called as root by `docker exec`; becomes the unprivileged `agent` with
# NET_ADMIN and NET_RAW removed from the bounding set for good, so that neither
# the agent nor `sudo` can change the firewall installed by the entrypoint.
#
# This runs as root with NET_ADMIN, before the privileges are dropped, so it must
# not look anything up in PATH: the image's PATH starts with folders the agent can
# write (~/.local/bin, the mise shims), and an `id` or `setpriv` put there would run
# as root. Both programs are called by absolute path. PATH itself stays as it is,
# because the session that starts below needs it.
set -e
gid="$(/usr/bin/id -g agent)"
exec /usr/bin/setpriv --reuid agent --regid "$gid" --init-groups \
  --bounding-set -net_admin,-net_raw --inh-caps -all -- "$@"
