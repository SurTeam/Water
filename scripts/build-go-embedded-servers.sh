#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python_bin="${WATER_PYTHON:-${HOME}/.venv/bin/python}"
if [[ ! -x "$python_bin" ]]; then python_bin="$(command -v python3)"; fi
exec "$python_bin" "$repo_root/scripts/build-go-embedded-servers.py" "$@"
