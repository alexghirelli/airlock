#!/bin/bash
# airlock-agent-install <pi|claude>: makes sure the agent is installed at the version
# /etc/airlock/manifest.json asks for. Idempotent and quick when it already is.
#
# Pi is in the image at one version; another version goes into the tools volume.
# Claude Code is never in the image (it cannot be redistributed): Anthropic's own
# installer puts it in the volume, in a folder per version, so projects share the
# download and can use different versions. "latest" is checked again once a day.
set -euo pipefail

kind="${1:?usage: airlock-agent-install pi|claude}"
MANIFEST=/etc/airlock/manifest.json
agents="${HOME:-/home/agent}/.local/share/mise/agents"
say() { echo "airlock: $*" >&2; }

ver="$(jq -r --arg k "$kind" '.agent.versions[$k] // empty' "$MANIFEST")"
[[ -n "$ver" ]] || { say "no version for $kind in the manifest"; exit 1; }

# A "latest" install is stale after a day; a pinned one never is.
fresh() { # marker
  [[ -e "$1" ]] && { [[ "$ver" != latest ]] || [[ -z "$(find "$1" -mmin +1440 2>/dev/null)" ]]; }
}

case "$kind" in
  pi)
    [[ "$ver" != "$(cat /opt/pi/VERSION)" ]] || exit 0
    dir="$agents/pi/$ver"
    fresh "$dir/.installed" && exit 0
    say "installing Pi $ver"
    mkdir -p "$dir"
    /opt/pi/shims/npm install -g --ignore-scripts --prefix "$dir" "$(cat /opt/pi/PACKAGE)@$ver" >&2
    touch "$dir/.installed"
    ;;
  claude)
    # Skip Claude Code's first-run screens (theme, trusting the folder, the
    # permission-bypass warning when the config asks for that mode), which Herdr
    # would otherwise wait on. Login is
    # still needed once: airlock login.
    cfg="${CLAUDE_CONFIG_DIR:-/creds/claude}"
    mkdir -p "$cfg"
    [[ -s "$cfg/.claude.json" ]] || echo '{}' >"$cfg/.claude.json"
    # The project is what this sandbox exists for, so it is trusted inside it (worktrees
    # under it inherit that).
    jq --arg dir "$(jq -r '.project_dir // empty' "$MANIFEST")" \
      '.hasCompletedOnboarding = true | .theme = (.theme // "dark")
       | if $dir != "" then .projects[$dir].hasTrustDialogAccepted = true else . end' \
      "$cfg/.claude.json" >"$cfg/.claude.json.tmp" && mv "$cfg/.claude.json.tmp" "$cfg/.claude.json"
    if jq -e '(.agent.args // []) | index("--dangerously-skip-permissions")' "$MANIFEST" >/dev/null; then
      [[ -s "$cfg/settings.json" ]] || echo '{}' >"$cfg/settings.json"
      jq '.skipDangerousModePermissionPrompt = true' "$cfg/settings.json" >"$cfg/settings.json.tmp" \
        && mv "$cfg/settings.json.tmp" "$cfg/settings.json"
    fi
    dir="$agents/claude/$ver"
    fresh "$dir/.installed" && exit 0
    say "installing Claude Code $ver (first use; Anthropic's installer, kept in the tools volume)"
    mkdir -p "$dir/home"
    script="$(mktemp)"; trap 'rm -f "$script"' EXIT
    curl -fsSL https://claude.ai/install.sh -o "$script"
    HOME="$dir/home" bash "$script" "$ver" >&2
    [[ -x "$(readlink -f "$dir/home/.local/bin/claude")" ]] || { say "the installer did not produce a claude binary"; exit 1; }
    touch "$dir/.installed"
    # Lets Herdr see what Claude Code is doing (idempotent, lives in the credentials volume).
    herdr integration install claude >/dev/null 2>&1 || say "warning: 'herdr integration install claude' failed"
    ;;
  *) say "unknown agent $kind"; exit 2 ;;
esac
