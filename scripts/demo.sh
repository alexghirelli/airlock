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
# A host home with decoy secrets, so the recording can show what the sandbox does not see.
mkdir -p "$work/home/.ssh" "$work/home/.aws"
echo "decoy, not a real key" > "$work/home/.ssh/id_ed25519"
echo "decoy, not a real credential" > "$work/home/.aws/credentials"

# A small project with a remote that has a pull request (refs/pull/7/head) on it.
git init -q --bare -b main "$work/origin.git"
git init -q -b main "$app"
git -C "$app" remote add origin "$work/origin.git"
cat > "$app/pagination.js" <<'JS'
// Returns the items of page `page` (1-based).
function paginate(items, page, size) {
  const start = page * size; // bug: page is 1-based
  return items.slice(start, start + size);
}
module.exports = { paginate };
JS
printf '# myapp\n\nA small API.\n' > "$app/README.md"
g() { git -C "$app" -c user.name="$1" -c user.email="$1@example.org" "${@:2}"; }
g Ada add -A && g Ada commit -q -m "Add pagination" && g Ada push -q origin main
g Grace checkout -q -b fix
sed -i 's/page \* size; \/\/ bug: page is 1-based/(page - 1) * size;/' "$app/pagination.js"
g Grace commit -q -am "Fix off-by-one in pagination"
g Grace push -q origin fix:refs/pull/7/head
g Grace checkout -q main && g Grace branch -q -D fix

# Logins and settings of the demo live in a volume of its own, removed with the sandbox,
# so what is on your machine does not show up in the recording.
export AIRLOCK_CREDENTIALS_SCOPE=per-project

# A ready sandbox, so the recording starts at the first command.
# (with the same HOME the recording uses, or airlock sees a changed configuration)
HOME="$work/home" "$work/bin/airlock" exec "$app" -- airlock-herdr-up --no-focus   # the project's own Herdr workspace

export AIRLOCK_DEMO_DIR="$app" AIRLOCK_DEMO_HOME="$work/home"
export PATH="$work/bin:$PATH"
vhs assets/demo.tape
ls -lh assets/demo.gif
