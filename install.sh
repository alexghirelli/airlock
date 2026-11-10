#!/bin/sh
# Installs the airlock binary from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/alexghirelli/airlock/main/install.sh | sh
#
# Environment:
#   AIRLOCK_VERSION      a release such as v0.1.0 (default: the latest)
#   AIRLOCK_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   AIRLOCK_BASE_URL     download from here instead of GitHub (testing, mirrors)
#
# The archive's SHA-256 is checked against the release's SHA256SUMS. That guards
# against corrupted downloads; it does not replace trusting the release itself.
# Windows: download the .zip from the release page, or run this inside WSL.
set -eu

repo="alexghirelli/airlock"
dir="${AIRLOCK_INSTALL_DIR:-$HOME/.local/bin}"

fail() { printf 'airlock install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "'$1' is required"; }

need curl; need tar; need uname
case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported system $(uname -s); on Windows use the .zip from the release page or WSL" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

tag="${AIRLOCK_VERSION:-}"
if [ -z "$tag" ]; then
  # /releases/latest redirects to /releases/tag/<tag>
  tag="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")" || fail "cannot find the latest release"
  tag="${tag##*/}"
fi
case "$tag" in v[0-9]*) ;; *) fail "unexpected release name '$tag'" ;; esac
version="${tag#v}"

base="${AIRLOCK_BASE_URL:-https://github.com/$repo/releases/download/$tag}"
file="airlock_${version}_${os}_${arch}.tar.gz"

if command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else fail "sha256sum or shasum is required to verify the download"; fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'Downloading airlock %s for %s/%s...\n' "$tag" "$os" "$arch"
curl -fsSL "$base/$file" -o "$tmp/$file" || fail "download failed: $base/$file"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || fail "download failed: $base/SHA256SUMS"

want="$(grep "  $file\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)"
[ -n "$want" ] || fail "$file is not listed in SHA256SUMS"
[ "$(sha "$tmp/$file")" = "$want" ] || fail "checksum mismatch for $file; not installing"

tar -xzf "$tmp/$file" -C "$tmp"
mkdir -p "$dir"
cp "$tmp/airlock_${version}_${os}_${arch}/airlock" "$dir/airlock"
chmod 0755 "$dir/airlock"
printf 'Installed %s\n' "$dir/airlock"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) printf '\n%s is not in your PATH. Add this to your shell profile:\n  export PATH="%s:$PATH"\n' "$dir" "$dir" ;;
esac
command -v docker >/dev/null 2>&1 || printf '\nNote: airlock needs Docker (or Docker Desktop, OrbStack, Colima). It was not found.\n'
printf '\nNext: cd into a project folder and run  airlock\n'
