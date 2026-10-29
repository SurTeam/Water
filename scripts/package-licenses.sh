#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
destination="${1:?usage: package-licenses.sh DESTINATION}"
cd "$root_dir"
# Preserve notices from the source copies actually compiled into the package.
for source in third_party/ebiten/LICENSE third_party/ebiten/NOTICE.md \
  third_party/typesetting/LICENSE third_party/typesetting/WATER_CHANGES.md third_party/typesetting/harfbuzz/LICENSE \
  internal/xterm/LICENSE internal/goserver/webassets/xterm-LICENSE.txt \
  internal/goserver/webassets/jsqr-LICENSE.txt internal/goserver/webassets/fit-LICENSE.txt; do
  mkdir -p "$destination/$(dirname "$source")"
  install -m 644 "$root_dir/$source" "$destination/$source"
done
install -m 644 "$root_dir/third_party/ebiten/WATER_CHANGES.md" "$destination/third_party/ebiten/WATER_CHANGES.md"
# Include licenses/notices from the resolved Go module graph.
"${PYTHON:-python3}" - "$destination" <<'PY'
from pathlib import Path
import shutil
import subprocess
import sys
destination = Path(sys.argv[1])
toolchain = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
for name in ('LICENSE', 'PATENTS'):
    source = toolchain / name
    if source.is_file():
        target = destination / 'go-stdlib' / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
modules = subprocess.check_output(['go', 'list', '-m', '-f', '{{.Path}}|{{.Dir}}', 'all'], text=True)
for entry in modules.splitlines():
    module, _, directory = entry.partition('|')
    if not directory:
        continue
    for notice in Path(directory).iterdir():
        if notice.is_file() and notice.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE')):
            target = destination / 'go' / module / notice.name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(notice, target)
PY
