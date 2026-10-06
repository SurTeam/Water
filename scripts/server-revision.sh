#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python_bin="${WATER_PYTHON:-${HOME}/.venv/bin/python}"
if [[ ! -x "$python_bin" ]]; then python_bin="$(command -v python3)"; fi
"$python_bin" "$root/scripts/server-revision.py"
