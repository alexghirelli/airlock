#!/bin/sh
# npm on Pi's private Node. Pi uses it to install Pi packages (see the
# npmCommand setting); it is deliberately not on the default PATH, so it never
# competes with the project's own toolchain.
export PATH=/opt/pi/shims:$PATH
exec /opt/pi/node /opt/pi/npm/bin/npm-cli.js "$@"
