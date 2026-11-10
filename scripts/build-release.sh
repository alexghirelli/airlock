#!/usr/bin/env bash
# Builds the release archives into ./dist for every supported platform, plus a
# SHA256SUMS file.   usage: scripts/build-release.sh 0.1.0
set -euo pipefail

version="${1:?usage: scripts/build-release.sh <version, e.g. 0.1.0>}"
version="${version#v}"
cd "$(dirname "$0")/.."

mod="$(go list -m)"
out=dist
rm -rf "$out"
mkdir -p "$out"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  name="airlock_${version}_${os}_${arch}"
  bin=airlock; [[ "$os" == windows ]] && bin=airlock.exe
  mkdir -p "$out/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X ${mod}/internal/version.Version=${version}" \
    -o "$out/$name/$bin" ./cmd/airlock
  cp LICENSE NOTICE README.md "$out/$name/"
  if [[ "$os" == windows ]]; then
    python3 - "$out" "$name" <<'PY'
import sys, zipfile, os
out, name = sys.argv[1:3]
with zipfile.ZipFile(f"{out}/{name}.zip", "w", zipfile.ZIP_DEFLATED) as z:
    for f in sorted(os.listdir(f"{out}/{name}")):
        z.write(f"{out}/{name}/{f}", f"{name}/{f}")
PY
  else
    tar -C "$out" --owner=0 --group=0 --numeric-owner -czf "$out/$name.tar.gz" "$name"
  fi
  rm -rf "${out:?}/$name"
  echo "built $name"
done

(cd "$out" && sha256sum airlock_* > SHA256SUMS)
echo "wrote $out/SHA256SUMS"
