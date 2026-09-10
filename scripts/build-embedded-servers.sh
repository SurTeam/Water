#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
variant="${WATER_APP_VARIANT:-dev}"
case "$variant" in
  dev) profile=debug; profile_args=(--profile dev) ;;
  release) profile=release; profile_args=(--release) ;;
  *) echo "error: WATER_APP_VARIANT must be dev or release" >&2; exit 1 ;;
esac
bundle_dir="${WATER_SERVER_BUNDLE_DIR:-$root_dir/target/embedded-servers/$variant}"
rust_toolchain="${WATER_RUST_TOOLCHAIN:-stable}"
rustc_path="$(rustup which rustc --toolchain "$rust_toolchain")"
targets=(
  aarch64-apple-darwin
  x86_64-apple-darwin
  aarch64-unknown-linux-musl
  x86_64-unknown-linux-musl
)
temporary=""
cleanup() {
  if [[ -n "$temporary" ]]; then
    rm -f -- "$temporary"
  fi
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$bundle_dir"
cd "$root_dir"

for target in "${targets[@]}"; do
  if ! rustup target list --toolchain "$rust_toolchain" --installed | rg -Fxq "$target"; then
    rustup target add --toolchain "$rust_toolchain" "$target"
  fi

  echo "Building headless water-server for $target"
  if [[ "$target" == *-unknown-linux-musl ]]; then
    env -u WATER_SERVER_BUNDLE_DIR -u WATER_REQUIRE_EMBEDDED_SERVERS \
      WATER_BUILDING_PORTABLE_SERVER=1 RUSTC="$rustc_path" RUSTFLAGS="-C strip=symbols" \
      rustup run "$rust_toolchain" cargo zigbuild "${profile_args[@]}" --no-default-features \
        --bin water-server --target "$target"
  elif [[ "$target" == *-apple-darwin && "$(uname -s)" != "Darwin" ]]; then
    # Cross-compile on non-macOS hosts via zig cc (scripts/zig-cc-mac)
    if ! command -v zig >/dev/null; then
      echo "error: zig is required to cross-compile $target on $(uname -s)" >&2
      exit 1
    fi
    env -u WATER_SERVER_BUNDLE_DIR -u WATER_REQUIRE_EMBEDDED_SERVERS \
      WATER_BUILDING_PORTABLE_SERVER=1 \
      RUSTFLAGS="-C strip=symbols -C linker=$root_dir/scripts/zig-cc-mac" \
      rustup run "$rust_toolchain" cargo build "${profile_args[@]}" --no-default-features \
        --bin water-server --target "$target"
  else
    env -u WATER_SERVER_BUNDLE_DIR -u WATER_REQUIRE_EMBEDDED_SERVERS \
      WATER_BUILDING_PORTABLE_SERVER=1 RUSTC="$rustc_path" RUSTFLAGS="-C strip=symbols" \
      rustup run "$rust_toolchain" cargo build "${profile_args[@]}" --no-default-features \
        --bin water-server --target "$target"
  fi

  source_binary="$root_dir/target/$target/$profile/water-server"
  destination="$bundle_dir/water-server-$target.gz"
  temporary="$bundle_dir/.water-server-$target.$$.gz"
  gzip -9 -n -c "$source_binary" >"$temporary"
  if [[ -f "$destination" ]] && cmp -s "$temporary" "$destination"; then
    rm -f "$temporary"
  else
    mv -f "$temporary" "$destination"
  fi
  temporary=""
done

printf '%s\n' "$variant" > "$bundle_dir/variant"
echo "Embedded $variant server payloads are ready in $bundle_dir"
