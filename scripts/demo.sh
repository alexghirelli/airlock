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

# A fixed path, so the GIF shows the same one every time (the project is mounted at
# the same absolute path inside the sandbox).
work=/tmp/airlock-demo
"$bin" rm "$work/myapp" >/dev/null 2>&1 || true
rm -rf "$work"
cleanup() {
  # airlock rm keeps a per-project credentials volume; this one is the demo's own.
  vols="$(docker ps -a -q --filter "label=io.airlock.project=$work/myapp" | xargs -r docker inspect --format '{{range .Mounts}}{{.Name}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep '^airlock-creds-myapp-' || true)"
  "$bin" rm "$work/myapp" >/dev/null 2>&1 || true
  [[ -z "$vols" ]] || docker volume rm $vols >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
mkdir -p "$work/bin" "$work/myapp"
ln -s "$(readlink -f "$bin")" "$work/bin/airlock"

app="$work/myapp"
git -C "$app" init -q
git -C "$app" -c user.name=demo -c user.email=demo@example.org commit -q --allow-empty -m "Initial commit"
printf '# myapp\n\nA small project to try airlock on.\n' > "$app/README.md"

# Logins and settings of the demo live in a volume of its own, removed with the sandbox,
# so what is on your machine does not show up in the recording.
export AIRLOCK_CREDENTIALS_SCOPE=per-project

# A ready sandbox, so the recording starts at the first command.
"$work/bin/airlock" exec "$app" -- true

export AIRLOCK_DEMO_DIR="$app"
export PATH="$work/bin:$PATH"
vhs assets/demo.tape
ls -lh assets/demo.gif
