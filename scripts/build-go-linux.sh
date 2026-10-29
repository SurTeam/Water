#!/usr/bin/env bash
set -euo pipefail

# Linux package build using dynamic musl and target-system X11/Mesa libraries.
#
#   GOARCH              amd64 or arm64 (default: host architecture)
#   CC / CXX            explicit target Linux C/C++ compiler commands
#   WATER_LINUX_SYSROOT Alpine musl target headers, CRT and shared libraries
#   WATER_LINUX_LIBC    must be musl
#   PKG_CONFIG          target-aware pkg-config for shared libraries
#
# On macOS, first run scripts/prepare-linux-sysroot.py <arch> and source
# target/linux-musl-dynamic/<arch>/env.sh. No Linux executables are run.
# The resulting package uses the target's standard /lib/ld-musl-*.so.1,
# X11 libraries and Mesa EGL/GL runtime (install through Alpine apk).
# Custom toolchains must likewise use dynamic musl and its matching CRT.
#
# Environment:
#   WATER_APP_VARIANT   dev (default) or release
#   WATER_APP_VERSION   product version (default: VERSION)

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

if [[ "${WATER_LINUX_LIBC:-}" != "musl" ]]; then
  echo "error: Linux packages require WATER_LINUX_LIBC=musl and a musl target toolchain" >&2
  echo "Run scripts/prepare-linux-sysroot.py <amd64|arm64> and source its env.sh." >&2
  exit 1
fi
if [[ -z "${CC:-}" || -z "${CXX:-}" || -z "${PKG_CONFIG:-}" || -z "${WATER_LINUX_SYSROOT:-}" ]]; then
  echo "error: Linux packaging requires CC, CXX, PKG_CONFIG and WATER_LINUX_SYSROOT" >&2
  echo "Run scripts/prepare-linux-sysroot.py <amd64|arm64> and source its env.sh." >&2
  exit 1
fi
[[ -d "$WATER_LINUX_SYSROOT/usr/include" ]] || {
  echo "error: WATER_LINUX_SYSROOT lacks usr/include" >&2; exit 1;
}
compiler_target="$($CC -dumpmachine)"
case "$compiler_target" in
  *linux-musl*) ;;
  *) echo "error: Linux compiler must target musl, got: $compiler_target" >&2; exit 1 ;;
esac
if [[ "${CGO_LDFLAGS:-}" == *-static* ]]; then
  echo "error: static libc is incompatible with the GUI's dynamic library loading" >&2
  exit 1
fi

variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in
  dev)
    package_name="water-dev"
    gui_installed_name="water-dev"
    server_installed_name="water-srv-dev"
    supervisor_installed_name="water-pty-dev"
    updater_installed_name="water-update-dev"
    ;;
  release)
    package_name="water"
    gui_installed_name="water"
    server_installed_name="water-server"
    supervisor_installed_name="water-pty"
    updater_installed_name="water-update"
    ;;
  *)
    echo "error: WATER_APP_VARIANT must be dev or release" >&2
    exit 1
    ;;
esac

version="${WATER_APP_VERSION:-$(cat "$root_dir/VERSION")}"
[[ -n "$version" ]] || { echo "error: product version is empty" >&2; exit 1; }

target_arch="${GOARCH:-$(go env GOHOSTARCH)}"
case "$target_arch" in
  arm64) arch=aarch64 ;;
  amd64) arch=x86_64 ;;
  *) echo "error: unsupported Linux architecture: $target_arch" >&2; exit 1 ;;
esac
export GOOS=linux GOARCH="$target_arch" CGO_ENABLED=1

identity_pkg="github.com/SurTeam/Water/internal/gobuild"
server_revision="$(bash "$root_dir/scripts/server-revision.sh")"
ldflags="-linkmode=external -X ${identity_pkg}.Variant=${variant} -X ${identity_pkg}.Version=${version} -X ${identity_pkg}.ServerRevision=${server_revision}"
if [[ "$variant" == "release" ]]; then
  ldflags="-s -w ${ldflags}"
fi

build_dir="$root_dir/target/go-linux/$variant/$target_arch"
binary_dir="$build_dir/bin"
stage_dir="$build_dir/package"
dist_dir="$root_dir/dist"
archive="$dist_dir/$package_name-$version-$arch-linux.tar.gz"

mkdir -p "$binary_dir" "$dist_dir"
WATER_APP_VARIANT="$variant" WATER_APP_VERSION="$version" \
  bash "$root_dir/scripts/build-go-embedded-servers.sh"

# Gio is used for offscreen screenshots; the window backend is Ebitengine X11.
go build -trimpath -tags=novulkan,nowayland -ldflags="$ldflags" -o "$binary_dir/water" ./cmd/water
go build -trimpath -ldflags="$ldflags" -o "$binary_dir/water-server" ./cmd/water-server
go build -trimpath -ldflags="$ldflags" -o "$binary_dir/water-update" ./cmd/water-update

rm -rf "$stage_dir"
mkdir -p "$stage_dir"
install -m 755 "$binary_dir/water" "$stage_dir/$gui_installed_name"
install -m 755 "$binary_dir/water-server" "$stage_dir/$server_installed_name"
# Give the PTY owner a distinct process name while sharing the server build.
install -m 755 "$binary_dir/water-server" "$stage_dir/$supervisor_installed_name"
install -m 755 "$binary_dir/water-update" "$stage_dir/$updater_installed_name"
bash "$root_dir/scripts/package-licenses.sh" "$stage_dir/Licenses"
if [[ -d "$WATER_LINUX_SYSROOT/usr/share/water-musl-licenses" ]]; then
  cp -R "$WATER_LINUX_SYSROOT/usr/share/water-musl-licenses" "$stage_dir/Licenses/linux-musl"
fi

rm -f "$archive"
tar -C "$stage_dir" -czf "$archive" "$gui_installed_name" "$server_installed_name" "$supervisor_installed_name" "$updater_installed_name" Licenses

echo "Built $archive"
echo "Run with: tar xzf $(basename "$archive") && ./$gui_installed_name"
