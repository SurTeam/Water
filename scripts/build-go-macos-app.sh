#!/usr/bin/env bash
set -euo pipefail

# Native macOS Water.app build for the Go rewrite.
#
# Environment:
#   WATER_APP_VARIANT   dev (default) or release
#   WATER_APP_VERSION   product version (default: VERSION)
#   CODESIGN_IDENTITY   codesign identity (default: - for ad-hoc)
#   CODESIGN_REQUIRED   require a non-ad-hoc identity (default: 0)
#   CODESIGN_SKIP       leave the bundle unsigned (default: 0)

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: the Ebitengine macOS app must be built natively on macOS" >&2
  exit 1
fi

variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in
  dev)
    app_name="Water Dev"
    bundle_id="dev.water.terminal.dev"
    gui_installed_name="water-dev"
    server_installed_name="water-srv-dev"
    updater_installed_name="water-update-dev"
    ;;
  release)
    app_name="Water"
    bundle_id="dev.water.terminal"
    gui_installed_name="water"
    server_installed_name="water-server"
    updater_installed_name="water-update"
    ;;
  *)
    echo "error: WATER_APP_VARIANT must be dev or release" >&2
    exit 1
    ;;
esac

version="${WATER_APP_VERSION:-$(cat "$root_dir/VERSION")}"
[[ -n "$version" ]] || { echo "error: product version is empty" >&2; exit 1; }

identity_pkg="github.com/SurTeam/Water/internal/gobuild"
ldflags="-X ${identity_pkg}.Variant=${variant} -X ${identity_pkg}.Version=${version}"
if [[ "$variant" == "release" ]]; then
  ldflags="-s -w ${ldflags}"
fi

build_dir="$root_dir/target/go-app/$variant"
app_dir="$root_dir/dist/${app_name}.app"
contents_dir="$app_dir/Contents"
macos_dir="$contents_dir/MacOS"
resources_dir="$contents_dir/Resources"

mkdir -p "$build_dir"
WATER_APP_VARIANT="$variant" WATER_APP_VERSION="$version"   bash "$root_dir/scripts/build-go-embedded-servers.sh"

go build -trimpath -ldflags="$ldflags" -o "$build_dir/water" ./cmd/water
go build -trimpath -ldflags="$ldflags" -o "$build_dir/water-server" ./cmd/water-server
go build -trimpath -ldflags="$ldflags" -o "$build_dir/water-update" ./cmd/water-update

rm -rf "$app_dir"
mkdir -p "$macos_dir" "$resources_dir"
install -m 755 "$build_dir/water" "$macos_dir/$gui_installed_name"
install -m 755 "$build_dir/water-server" "$macos_dir/$server_installed_name"
install -m 755 "$build_dir/water-update" "$macos_dir/$updater_installed_name"
install -m 644 "$root_dir/assets/macos/Water.icns" "$resources_dir/Water.icns"

install -d -m 755 "$resources_dir/skills/water-control"
install -m 644 "$root_dir/.agents/skills/water-control/SKILL.md"   "$resources_dir/skills/water-control/SKILL.md"

template="$root_dir/assets/macos/Info.plist.template"
if [[ ! -f "$template" ]]; then
  echo "error: missing $template" >&2
  exit 1
fi
sed -e "s/__WATER_VERSION__/$version/g"     -e "s/__BUNDLE_ID__/$bundle_id/g"     -e "s/__APP_NAME__/$app_name/g"     -e "s/__EXECUTABLE__/$gui_installed_name/g"     "$template" > "$contents_dir/Info.plist"

plutil -lint "$contents_dir/Info.plist" >/dev/null

codesign_identity="${CODESIGN_IDENTITY:--}"
codesign_required="${CODESIGN_REQUIRED:-0}"
codesign_skip="${CODESIGN_SKIP:-0}"
if [[ "$codesign_skip" != "0" && "$codesign_skip" != "1" ]]; then
  echo "error: CODESIGN_SKIP must be 0 or 1" >&2
  exit 1
fi
if [[ "$codesign_skip" == "1" ]]; then
  [[ "$codesign_required" == "0" ]] || {
    echo "error: CODESIGN_REQUIRED cannot be used with CODESIGN_SKIP=1" >&2
    exit 1
  }
elif [[ "$codesign_required" != "0" && "$codesign_identity" == "-" ]]; then
  echo "error: CODESIGN_REQUIRED is set but CODESIGN_IDENTITY is ad-hoc" >&2
  exit 1
elif [[ "$codesign_identity" == "-" ]]; then
  codesign --force --deep --sign - "$app_dir" >/dev/null
else
  codesign --force --options runtime --timestamp --sign "$codesign_identity" "$macos_dir/$updater_installed_name" >/dev/null
  codesign --force --options runtime --timestamp --sign "$codesign_identity"     "$macos_dir/$server_installed_name" >/dev/null
  codesign --force --options runtime --timestamp --sign "$codesign_identity"     "$macos_dir/$gui_installed_name" >/dev/null
  codesign --force --options runtime --timestamp --sign "$codesign_identity"     "$app_dir" >/dev/null
fi

if [[ "$codesign_skip" != "1" ]]; then
  codesign --verify --deep --strict "$app_dir"
fi

case "$(uname -m)" in
  arm64) archive_arch=arm64 ;;
  x86_64) archive_arch=amd64 ;;
  *) echo "error: unsupported macOS architecture" >&2; exit 1 ;;
esac
zip_name="${app_name}-${version}-macOS-${archive_arch}.zip"
zip_path="$root_dir/dist/$zip_name"
rm -f "$zip_path"
ditto -c -k --sequesterRsrc --keepParent "$app_dir" "$zip_path"
test -s "$zip_path"

echo "Built $app_dir"
echo "Built $zip_path"
