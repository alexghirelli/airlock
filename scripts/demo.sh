#!/usr/bin/env bash
# Renders assets/demo.gif from assets/demo.tape (charmbracelet/vhs).
#   usage: scripts/demo.sh [path-to-airlock-binary]
# It makes a throwaway project and starts its sandbox first, so the recording shows
# the commands and not the image download. Needs vhs, ttyd, ffmpeg and Docker.
set -euo pipefail
cd "$(dirname "$0")/.."

for tool in vhs ttyd ffmpeg docker; do
  command -v "$tool" >/dev/null || { echo "demo: $tool is not installed" >&2; exit 1; }
done

bin="${1:-}"
if [[ -z "$bin" ]]; then
  bin="$(command -v airlock || true)"
  [[ -n "$bin" ]] || { echo "demo: no airlock in PATH; pass the binary as an argument" >&2; exit 1; }
fi

work="$(mktemp -d)"
trap 'AIRLOCK_DEMO_DIR="$work/myapp" "$bin" rm "$work/myapp" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/myapp"
ln -s "$(readlink -f "$bin")" "$work/bin/airlock"

app="$work/myapp"
git -C "$app" init -q
git -C "$app" -c user.name=demo -c user.email=demo@example.org commit -q --allow-empty -m "Initial commit"
printf '# myapp\n\nA small project to try airlock on.\n' > "$app/README.md"

# A ready sandbox, so the recording starts at the first command.
"$work/bin/airlock" exec "$app" -- true

export AIRLOCK_DEMO_DIR="$app"
export PATH="$work/bin:$PATH"
vhs assets/demo.tape
ls -lh assets/demo.gif
