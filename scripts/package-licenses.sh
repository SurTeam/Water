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
# Upstream toolchains and downloaded module-cache toolchains keep LICENSE at
# the tree root; Homebrew keeps it beside libexec; distributions such as
# Arch and Fedora ship it under /usr/share/licenses instead of in GOROOT.
toolchain_candidates = (toolchain, toolchain.parent,
                        Path('/usr/share/licenses/go'),
                        Path('/usr/share/licenses/golang'))
for name in ('LICENSE', 'PATENTS'):
    source = next((base / name for base in toolchain_candidates
                   if (base / name).is_file()), None)
    if name == 'LICENSE' and source is None:
        raise SystemExit('Go toolchain LICENSE not found')
    if source is not None:
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
