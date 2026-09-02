#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: Water.app can only be assembled on macOS" >&2
  exit 1
fi

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

app_name="Water"
binary_name="water"
app_dir="$root_dir/dist/$app_name.app"
contents_dir="$app_dir/Contents"
macos_dir="$contents_dir/MacOS"
resources_dir="$contents_dir/Resources"
version="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"

cargo build --release --bin "$binary_name"

rm -rf "$app_dir"
mkdir -p "$macos_dir" "$resources_dir"
install -m 755 "$root_dir/target/release/$binary_name" "$macos_dir/$binary_name"
install -m 644 "$root_dir/assets/macos/Water.icns" "$resources_dir/Water.icns"
sed "s/__WATER_VERSION__/$version/g" "$root_dir/assets/macos/Info.plist" > "$contents_dir/Info.plist"

plutil -lint "$contents_dir/Info.plist" >/dev/null

# Ad-hoc signing makes the local bundle internally consistent. Set
# CODESIGN_IDENTITY to a Developer ID/Application identity for distribution.
codesign_identity="${CODESIGN_IDENTITY:--}"
codesign --force --deep --sign "$codesign_identity" "$app_dir" >/dev/null
codesign --verify --deep --strict "$app_dir"

echo "Built $app_dir"
echo "Launch with: open '$app_dir'"
