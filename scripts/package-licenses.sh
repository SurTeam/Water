#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
destination="${1:?usage: package-licenses.sh DESTINATION}"
# Preserve notices from the source copies actually compiled into the package.
for source in third_party/ebiten/LICENSE third_party/ebiten/NOTICE.md \
  third_party/typesetting/LICENSE third_party/typesetting/harfbuzz/LICENSE \
  internal/xterm/LICENSE internal/goserver/webassets/xterm-LICENSE.txt \
  internal/goserver/webassets/jsqr-LICENSE.txt internal/goserver/webassets/fit-LICENSE.txt; do
  mkdir -p "$destination/$(dirname "$source")"
  install -m 644 "$root_dir/$source" "$destination/$source"
done
install -m 644 "$root_dir/third_party/ebiten/WATER_CHANGES.md" "$destination/third_party/ebiten/WATER_CHANGES.md"
