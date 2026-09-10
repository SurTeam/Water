#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

binary_name="water"
dist_dir="$root_dir/dist"
arch="$(uname -m)"
case "$arch" in
  aarch64 | arm64) arch="aarch64" ;;
  x86_64 | amd64) arch="x86_64" ;;
  *) echo "error: unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac
version="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
server_bundle_dir="$root_dir/target/embedded-servers"

WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" "$root_dir/scripts/build-embedded-servers.sh"
WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
  cargo build --release --bin "$binary_name"

rm -rf "$dist_dir"
mkdir -p "$dist_dir"
tar -C "$root_dir/target/release" -czf "$dist_dir/$binary_name-${version}-${arch}-linux.tar.gz" "$binary_name"

echo "Built $dist_dir/$binary_name-${version}-${arch}-linux.tar.gz"
echo "Run with: tar xzf $dist_dir/$binary_name-${version}-${arch}-linux.tar.gz && ./$binary_name"