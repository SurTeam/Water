#!/usr/bin/env bash
set -euo pipefail

# Native Linux package build for the Go rewrite.
#
# Environment:
#   WATER_APP_VARIANT   dev (default) or release
#   WATER_APP_VERSION   product version (default: Cargo.toml package version)

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "error: the Gio Linux package must be built natively on Linux" >&2
  exit 1
fi

variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in
  dev)
    package_name="water-dev"
    gui_installed_name="water-dev"
    server_installed_name="water-srv-dev"
    ;;
  release)
    package_name="water"
    gui_installed_name="water"
    server_installed_name="water-server"
    ;;
  *)
    echo "error: WATER_APP_VARIANT must be dev or release" >&2
    exit 1
    ;;
esac

version="${WATER_APP_VERSION:-$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)}"
[[ -n "$version" ]] || { echo "error: product version is empty" >&2; exit 1; }

arch="$(uname -m)"
case "$arch" in
  aarch64 | arm64) arch="aarch64" ;;
  x86_64 | amd64) arch="x86_64" ;;
  *) echo "error: unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac

identity_pkg="github.com/SurTeam/Water/internal/gobuild"
ldflags="-X ${identity_pkg}.Variant=${variant} -X ${identity_pkg}.Version=${version}"
if [[ "$variant" == "release" ]]; then
  ldflags="-s -w ${ldflags}"
fi

build_dir="$root_dir/target/go-linux/$variant"
stage_dir="$build_dir/$package_name"
dist_dir="$root_dir/dist"
archive="$dist_dir/$package_name-$version-$arch-linux.tar.gz"

mkdir -p "$build_dir" "$dist_dir"
WATER_APP_VARIANT="$variant" WATER_APP_VERSION="$version" \
  bash "$root_dir/scripts/build-go-embedded-servers.sh"

go build -trimpath -ldflags="$ldflags" -o "$build_dir/water" ./cmd/water
go build -trimpath -ldflags="$ldflags" -o "$build_dir/water-server" ./cmd/water-server

rm -rf "$stage_dir"
mkdir -p "$stage_dir"
install -m 755 "$build_dir/water" "$stage_dir/$gui_installed_name"
install -m 755 "$build_dir/water-server" "$stage_dir/$server_installed_name"

rm -f "$archive"
tar -C "$stage_dir" -czf "$archive" "$gui_installed_name" "$server_installed_name"

echo "Built $archive"
echo "Run with: tar xzf $(basename "$archive") && ./$gui_installed_name"
