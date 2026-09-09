#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bundle_dir="${WATER_SERVER_BUNDLE_DIR:-$root_dir/target/embedded-servers}"
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
      rustup run "$rust_toolchain" cargo zigbuild --release --no-default-features \
        --bin water-server --target "$target"
  else
    env -u WATER_SERVER_BUNDLE_DIR -u WATER_REQUIRE_EMBEDDED_SERVERS \
      WATER_BUILDING_PORTABLE_SERVER=1 RUSTC="$rustc_path" RUSTFLAGS="-C strip=symbols" \
      rustup run "$rust_toolchain" cargo build --release --no-default-features \
        --bin water-server --target "$target"
  fi

  source_binary="$root_dir/target/$target/release/water-server"
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

echo "Embedded server payloads are ready in $bundle_dir"
