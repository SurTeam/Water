#!/usr/bin/env bash
set -euo pipefail

# Compile shaders.metallib on a Mac so Linux cross-compile can use it.
# Run this once on macOS, then commit the output to scripts/prebuilt/.
#
# Usage:
#   scripts/build-metallib.sh
#
# Output:
#   scripts/prebuilt/shaders.metallib

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

out_dir="$root_dir/scripts/prebuilt"
mkdir -p "$out_dir"

shader_src="$root_dir/scripts/stub-gpui_apple/src/shaders.metal"

# We need the generated scene.h header. Run the build script's cbindgen step
# by doing a full cargo build on macOS first (which generates it in OUT_DIR),
# then extract it. Alternatively, compile the shader with -std=macos-version
# and let the metal compiler handle the includes.

# Simpler: just compile the .metal file directly. The shaders.metal file
# includes the scene types via a generated header, but for metallib
# compilation we can use the -std flag and let the compiler resolve types.

# Actually, the build.rs generates scene.h via cbindgen and passes it as
# -include. Let's replicate that: run cargo build once to generate the header,
# then use it for metal compilation.

echo "Step 1: Generating scene.h via cargo build (macOS)..."
build_log=$(mktemp)
cargo build --release 2>"$build_log" || {
  echo "cargo build failed:" >&2
  tail -20 "$build_log" >&2
  rm -f "$build_log"
  exit 1
}
rm -f "$build_log"

# Find the generated scene.h in the target directory
scene_h=$(find target/release/build -name "scene.h" -path "*/gpui_apple*/out/scene.h" 2>/dev/null | head -1)
if [[ -z "$scene_h" ]]; then
  # Try the stub-gpui_apple OUT_DIR
  scene_h=$(find target/release/build -name "scene.h" 2>/dev/null | head -1)
fi
if [[ -z "$scene_h" ]]; then
  echo "error: could not find generated scene.h" >&2
  exit 1
fi
echo "Found scene.h: $scene_h"

# Copy shader to a temp location (metal compiler embeds the input path)
tmp_dir=$(mktemp -d)
staged_shader="$tmp_dir/shaders.metal"
cp "$shader_src" "$staged_shader"

air_path="$tmp_dir/shaders.air"
metallib_path="$out_dir/shaders.metallib"

echo "Step 2: Compiling Metal shader..."
xcrun -sdk macosx metal \
  -gline-tables-only \
  -mmacosx-version-min=12.0 \
  -MO \
  -c \
  "$staged_shader" \
  -include "$scene_h" \
  -o "$air_path"

echo "Step 3: Linking metallib..."
xcrun -sdk macosx metallib "$air_path" -o "$metallib_path"

rm -rf "$tmp_dir"

echo ""
echo "Done: $metallib_path"
echo "Size: $(du -h "$metallib_path" | cut -f1)"
echo ""
echo "Commit this file so Linux cross-compile can use it:"
echo "  git add scripts/prebuilt/shaders.metallib"