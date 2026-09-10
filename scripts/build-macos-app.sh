#!/usr/bin/env bash
set -euo pipefail

# Builds the Water.app bundle and a distributable zip.
#
# On macOS: builds natively (embedded servers + GUI), assembles, signs, zips.
# On Linux: cross-compiles aarch64-apple-darwin via zig cc, assembles, zips.
#           Requires: zig (>= 0.14) on PATH.
#
# Environment:
#   CODESIGN_IDENTITY     codesign identity (default: "-" ad-hoc)
#   WATER_RUST_TOOLCHAIN  rustup toolchain (default: stable)

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

app_name="Water"
binary_name="water"
app_dir="$root_dir/dist/$app_name.app"
contents_dir="$app_dir/Contents"
macos_dir="$contents_dir/MacOS"
resources_dir="$contents_dir/Resources"
version="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
server_bundle_dir="$root_dir/target/embedded-servers"
rust_toolchain="${WATER_RUST_TOOLCHAIN:-stable}"
target="aarch64-apple-darwin"

WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" "$root_dir/scripts/build-embedded-servers.sh"

if [[ "$(uname -s)" == "Darwin" ]]; then
  build_dir="$root_dir/target/release"
  WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
    cargo build --release --bin "$binary_name"
else
  # Cross-compile on Linux via zig cc
  if ! command -v zig >/dev/null; then
    echo "error: zig is required to cross-compile macOS on Linux" >&2
    exit 1
  fi
  build_dir="$root_dir/target/$target/release"
  export PATH="$root_dir/scripts/stub:$PATH"
  WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
    RUSTFLAGS="-C strip=symbols -C linker=$root_dir/scripts/zig-cc-mac" \
    rustup run "$rust_toolchain" cargo build --release --bin "$binary_name" --target "$target"
fi

rm -rf "$app_dir"
mkdir -p "$macos_dir" "$resources_dir"
install -m 755 "$build_dir/$binary_name" "$macos_dir/$binary_name"
install -m 755 "$root_dir/target/$target/release/water-server" \
  "$macos_dir/water-server"
install -m 644 "$root_dir/assets/macos/Water.icns" "$resources_dir/Water.icns"
sed "s/__WATER_VERSION__/$version/g" "$root_dir/assets/macos/Info.plist" > "$contents_dir/Info.plist"

if command -v plutil >/dev/null; then
  plutil -lint "$contents_dir/Info.plist" >/dev/null
else
  python3 -c "import xml.dom.minidom, sys; xml.dom.minidom.parse('$contents_dir/Info.plist')"
fi

codesign_identity="${CODESIGN_IDENTITY:--}"
if command -v codesign >/dev/null; then
  codesign --force --deep --sign "$codesign_identity" "$app_dir" >/dev/null
  codesign --verify --deep --strict "$app_dir"
else
  echo "note: codesign not found; skipping signature steps"
fi

zip_path="$root_dir/dist/$app_name-$version-macOS-arm64.zip"
rm -f "$zip_path"
(cd "$root_dir/dist" && zip -r -X -q "$zip_path" "$app_name.app")

echo "Built $app_dir"
echo "Built $zip_path"