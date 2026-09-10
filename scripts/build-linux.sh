#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in
  dev) profile=debug; profile_args=(--profile dev); package_name=water-dev ;;
  release) profile=release; profile_args=(--release); package_name=water ;;
  *) echo "error: WATER_APP_VARIANT must be dev or release" >&2; exit 1 ;;
esac
binary_name="water"
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
  cargo build "${profile_args[@]}" --bin "$binary_name" --bin water-server --bin waterctl

mkdir -p "$dist_dir"
tar -C "$root_dir/target/$profile" -czf "$dist_dir/$package_name-${version}-${arch}-linux.tar.gz" "$binary_name" water-server waterctl

echo "Built $dist_dir/$package_name-${version}-${arch}-linux.tar.gz"
echo "Run with: tar xzf $dist_dir/$package_name-${version}-${arch}-linux.tar.gz && ./$binary_name"
