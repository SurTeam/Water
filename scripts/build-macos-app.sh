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
#   WATER_APP_VARIANT     "dev" (default) or "release"
#                         dev: optimized dev profile, bundle ID *.dev, app name "Water Dev"

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in dev|release) ;; *) echo "error: WATER_APP_VARIANT must be dev or release" >&2; exit 1 ;; esac
if [[ "$variant" == "dev" ]]; then
  app_name="Water Dev"
  bundle_id="dev.water.terminal.dev"
  profile_flag="--profile dev"
  cargo_profile="debug"
else
  app_name="Water"
  bundle_id="dev.water.terminal"
  profile_flag="--release"
  cargo_profile="release"
fi

binary_name="water"
app_dir="$root_dir/dist/${app_name}.app"
contents_dir="$app_dir/Contents"
macos_dir="$contents_dir/MacOS"
resources_dir="$contents_dir/Resources"
version="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
server_bundle_dir="$root_dir/target/embedded-servers/$variant"
rust_toolchain="${WATER_RUST_TOOLCHAIN:-stable}"
target="aarch64-apple-darwin"

# Build embedded servers (needed for both variants)
WATER_APP_VARIANT="$variant" WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" bash "$root_dir/scripts/build-embedded-servers.sh"

if [[ "$variant" == "dev" ]]; then
  # Dev: optimized, independently branded binaries
  if [[ "$(uname -s)" == "Darwin" ]]; then
    build_dir="$root_dir/target/$cargo_profile"
    WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
      cargo build $profile_flag --bin "$binary_name" --bin water-server
  else
    if ! command -v zig >/dev/null; then
      echo "error: zig is required to cross-compile macOS on Linux" >&2
      exit 1
    fi
    build_dir="$root_dir/target/$target/$cargo_profile"
    export PATH="$root_dir/scripts/stub:$PATH"
    WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
      RUSTFLAGS="-C strip=symbols -C linker=$root_dir/scripts/zig-cc-mac" \
      rustup run "$rust_toolchain" cargo build $profile_flag --bin "$binary_name" --bin water-server --target "$target"
  fi
else
  # Release
  if [[ "$(uname -s)" == "Darwin" ]]; then
    build_dir="$root_dir/target/release"
    WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
      cargo build --release --bin "$binary_name" --bin water-server
  else
    if ! command -v zig >/dev/null; then
      echo "error: zig is required to cross-compile macOS on Linux" >&2
      exit 1
    fi
    build_dir="$root_dir/target/$target/release"
    export PATH="$root_dir/scripts/stub:$PATH"
    WATER_SERVER_BUNDLE_DIR="$server_bundle_dir" WATER_REQUIRE_EMBEDDED_SERVERS=1 \
      RUSTFLAGS="-C strip=symbols -C linker=$root_dir/scripts/zig-cc-mac" \
      rustup run "$rust_toolchain" cargo build --release --bin "$binary_name" --bin water-server --target "$target"
  fi
fi

# Assemble .app bundle
rm -rf "$app_dir"
mkdir -p "$macos_dir" "$resources_dir"

# Process names come from the executable file names so ps / Activity Monitor
# show water-dev / water-srv-dev (dev) or water / water-server (release)
# without relying on setprogname. The GUI locates the sibling by this name.
if [[ "$variant" == "dev" ]]; then
  gui_installed_name="water-dev"
  server_installed_name="water-srv-dev"
else
  gui_installed_name="water"
  server_installed_name="water-server"
fi
install -m 755 "$build_dir/$binary_name" "$macos_dir/$gui_installed_name"

# Install water-server from the matching target triple + profile
server_build_dir="$build_dir"
if [[ -f "$server_build_dir/water-server" ]]; then
  install -m 755 "$server_build_dir/water-server" "$macos_dir/$server_installed_name"
else
  echo "error: water-server not found at $server_build_dir/water-server" >&2
  exit 1
fi

install -m 644 "$root_dir/assets/macos/Water.icns" "$resources_dir/Water.icns"

# Bundle the water-control skill as a resource for agent tooling.
install -d -m 755 "$resources_dir/skills/water-control"
install -m 644 "$root_dir/.agents/skills/water-control/SKILL.md" \
  "$resources_dir/skills/water-control/SKILL.md"

# Generate Info.plist with the correct bundle ID and executable name
sed -e "s/__WATER_VERSION__/$version/g" \
    -e "s/__BUNDLE_ID__/$bundle_id/g" \
    -e "s/__APP_NAME__/$app_name/g" \
    -e "s/__EXECUTABLE__/$gui_installed_name/g" \
    "$root_dir/assets/macos/Info.plist.template" > "$contents_dir/Info.plist"

# Fallback: if no template, use the original with sed
if [[ ! -f "$root_dir/assets/macos/Info.plist.template" ]]; then
  sed -e "s/__WATER_VERSION__/$version/g" \
      -e "s/dev\.water\.terminal/$bundle_id/g" \
      "$root_dir/assets/macos/Info.plist" > "$contents_dir/Info.plist"
fi

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

zip_name="${app_name}-${version}-macOS-arm64.zip"
zip_path="$root_dir/dist/$zip_name"
rm -f "$zip_path"
(cd "$root_dir/dist" && zip -r -X -q "$zip_path" "${app_name}.app")

echo "Built $app_dir"
echo "Built $zip_path"
