#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in
  dev) profile=debug; profile_args=(--profile dev); package_name=water-dev; binary_name=water-dev; server_name=water-srv-dev ;;
  release) profile=release; profile_args=(--release); package_name=water; binary_name=water; server_name=water-server ;;
  *) echo "error: WATER_APP_VARIANT must be dev or release" >&2; exit 1 ;;
esac
dist_dir="$root_dir/dist"
arch="$(uname -m)"
case "$arch" in
  aarch64 | arm64) arch="aarch64" ;;
  x86_64 | amd64) arch="x86_64" ;;
  *) echo "error: unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac
version="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
server_bundle_dir="$root_dir/target/embedded-servers/$variant"

WATER_APP_VARIANT="$variant" WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" bash "$root_dir/scripts/build-embedded-servers.sh"
WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
  cargo build "${profile_args[@]}" --bin water --bin water-server --bin waterctl

# Rename the GUI + server so ps/kill show water-dev / water-srv-dev for dev
# builds without relying on prctl (which is not safe Rust).
cp -f "$root_dir/target/$profile/water" "$root_dir/target/$profile/$binary_name"
chmod 755 "$root_dir/target/$profile/$binary_name"
cp -f "$root_dir/target/$profile/water-server" "$root_dir/target/$profile/$server_name"
chmod 755 "$root_dir/target/$profile/$server_name"

mkdir -p "$dist_dir"
tar -C "$root_dir/target/$profile" -czf "$dist_dir/$package_name-${version}-${arch}-linux.tar.gz" "$binary_name" "$server_name" waterctl

echo "Built $dist_dir/$package_name-${version}-${arch}-linux.tar.gz"
echo "Run with: tar xzf $dist_dir/$package_name-${version}-${arch}-linux.tar.gz && ./$binary_name"
