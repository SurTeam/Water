#!/usr/bin/env bash
set -euo pipefail

# Cross-build Windows GUI packages on macOS or Linux; no Windows host required.
# Usage: WATER_APP_VARIANT=release bash scripts/build-go-windows.sh [all|amd64|arm64]
# PYTHON selects the Python 3 interpreter. Product version defaults to VERSION.
# WINDOWS_SIGN_REQUIRED=1 requires Authenticode credentials below. Unsigned is the dev default.
# WINDOWS_SIGN_P12, WINDOWS_SIGN_PASSWORD_FILE, WINDOWS_SIGN_CA_FILE enable signing.
# WINDOWS_SIGN_TIMESTAMP_URL optionally selects an RFC3161 timestamp service.
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"
variant="${WATER_APP_VARIANT:-dev}"
version="${WATER_APP_VERSION:-$(cat VERSION)}"
selection="${1:-all}"
python="${PYTHON:-python3}"
case "$variant" in
  dev) suffix=-dev; server_name=water-srv-dev.exe ;;
  release) suffix=; server_name=water-server.exe ;;
  *) echo 'error: WATER_APP_VARIANT must be dev or release' >&2; exit 1 ;;
esac
case "$selection" in
  all) architectures=(amd64 arm64) ;;
  amd64|arm64) architectures=("$selection") ;;
  *) echo 'usage: build-go-windows.sh [all|amd64|arm64]' >&2; exit 1 ;;
esac
[[ $# -le 1 && -n "$version" ]] || { echo 'error: invalid arguments or empty version' >&2; exit 1; }
sign_p12="${WINDOWS_SIGN_P12:-}"
if [[ -n "$sign_p12" ]]; then
  command -v osslsigncode >/dev/null
  [[ -f "$sign_p12" && -f "${WINDOWS_SIGN_PASSWORD_FILE:-}" && -f "${WINDOWS_SIGN_CA_FILE:-}" ]] || {
    echo 'error: signing requires P12, password file and verification CA file' >&2; exit 1;
  }
elif [[ "${WINDOWS_SIGN_REQUIRED:-0}" != 0 || -n "${WINDOWS_SIGN_PASSWORD_FILE:-}${WINDOWS_SIGN_CA_FILE:-}" ]]; then
  echo 'error: WINDOWS_SIGN_P12 is required for signing' >&2; exit 1
fi
export WATER_APP_VARIANT="$variant" WATER_APP_VERSION="$version"
"$python" scripts/build-go-embedded-servers.py
revision="$("$python" -c 'import json; print(json.load(open("internal/goremote/payloads/manifest.json"))["identity"]["server_revision"])')"
identity_pkg=github.com/SurTeam/Water/internal/gobuild
flags="-X $identity_pkg.Variant=$variant -X $identity_pkg.Version=$version -X $identity_pkg.ServerRevision=$revision"
if [[ "$variant" == release ]]; then flags="-s -w $flags"; fi
mkdir -p dist
for architecture in "${architectures[@]}"; do
  stage="$root_dir/target/go-windows/$variant/$architecture/package"
  rm -rf "$stage"
  mkdir -p "$stage"
  GOOS=windows GOARCH="$architecture" CGO_ENABLED=0 go build -trimpath -ldflags="$flags" -o "$stage/water$suffix.exe" ./cmd/water
  GOOS=windows GOARCH="$architecture" CGO_ENABLED=0 go build -trimpath -ldflags="$flags" -o "$stage/$server_name" ./cmd/water-server
  GOOS=windows GOARCH="$architecture" CGO_ENABLED=0 go build -trimpath -ldflags="$flags" -o "$stage/water-update$suffix.exe" ./cmd/water-update
  cp "$stage/$server_name" "$stage/water-pty$suffix.exe"
  if [[ -n "$sign_p12" ]]; then
    for executable in "$stage"/*.exe; do
      sign_args=(-pkcs12 "$sign_p12" -readpass "$WINDOWS_SIGN_PASSWORD_FILE" -h sha256 -n Water)
      if [[ -n "${WINDOWS_SIGN_TIMESTAMP_URL:-}" ]]; then sign_args+=(-ts "$WINDOWS_SIGN_TIMESTAMP_URL"); fi
      osslsigncode sign "${sign_args[@]}" -in "$executable" -out "$executable.signed"
      osslsigncode verify -CAfile "$WINDOWS_SIGN_CA_FILE" -in "$executable.signed"
      mv "$executable.signed" "$executable"
    done
  fi
  cp internal/goremote/payloads/manifest.json "$stage/payload-manifest.json"
  PYTHON="$python" bash scripts/package-licenses.sh "$stage/Licenses"
  archive="$root_dir/dist/water$suffix-$version-windows-$architecture.zip"
  "$python" - "$stage" "$archive" <<'PY'
import hashlib
from pathlib import Path
import sys
import zipfile
stage, archive = map(Path, sys.argv[1:])
with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as bundle:
    for path in sorted(stage.rglob('*')):
        if path.is_symlink():
            raise SystemExit('unexpected package symlink: ' + str(path))
        if path.is_file():
            bundle.write(path, path.relative_to(stage).as_posix())
with zipfile.ZipFile(archive) as bundle:
    if bundle.testzip() is not None:
        raise SystemExit('ZIP integrity check failed')
print('Built', archive)
print('SHA256', hashlib.sha256(archive.read_bytes()).hexdigest())
PY
done
