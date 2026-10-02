#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WATER_BIN="${WATER_BIN:-$ROOT/target/go-ui-smoke/water}"
TMP_ROOT="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/water-go-ui-smoke-$$"
SOCKET="$TMP_ROOT/water.sock"
CONFIG="$TMP_ROOT/config.json"
LOG="$TMP_ROOT/water-gui.log"
SNAPSHOT="$TMP_ROOT/ui-snapshot.json"
STATE_BEFORE="$TMP_ROOT/state-before.json"
STATE_AFTER="$TMP_ROOT/state-after.json"
SCREENSHOT="$TMP_ROOT/water.png"
mkdir -p "$TMP_ROOT"

gui_pid=""
cleanup() {
  if [[ -S "$SOCKET" ]]; then
    "$WATER_BIN" --socket "$SOCKET" server shutdown >/dev/null 2>&1 || true
  fi
  if [[ -n "$gui_pid" ]]; then
    kill "$gui_pid" >/dev/null 2>&1 || true
    wait "$gui_pid" >/dev/null 2>&1 || true
  fi
  if [[ "${WATER_KEEP_UI_SMOKE:-0}" != "1" ]]; then
    rm -rf "$TMP_ROOT"
  else
    echo "UI smoke files retained at $TMP_ROOT"
  fi
}
trap cleanup EXIT INT TERM

cat >"$CONFIG" <<'JSON'
{
  "startup": {
    "initial_workspace": true,
    "initial_terminal": true,
    "window_width": 960,
    "window_height": 640,
    "window_min_width": 640,
    "window_min_height": 400
  },
  "server": {
    "auto_start": true,
    "detached": false,
    "detach_on_quit": false
  },
  "shell": {
    "program": "/bin/sh",
    "args": ["-l"]
  }
}
JSON

"$WATER_BIN" --control-socket "$SOCKET" --config "$CONFIG" >"$LOG" 2>&1 &
gui_pid=$!

for _ in $(seq 1 240); do
  if "$WATER_BIN" --socket "$SOCKET" ping >/dev/null 2>&1; then
    break
  fi
  sleep 0.025
done
"$WATER_BIN" --socket "$SOCKET" ping >/dev/null

# Wait for a real Gio frame to populate actual clickable geometry.
hit=""
for _ in $(seq 1 240); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    hit="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
for hit in data.get("automation_hits", []):
    if hit.get("kind")=="new_workspace":
        x0,y0,x1,y1=hit["rect"]
        print(f"{(x0+x1)/2:.1f} {(y0+y1)/2:.1f}")
        break
PY
)"
    if [[ -n "$hit" ]]; then
      break
    fi
  fi
  sleep 0.025
done

if [[ -z "$hit" ]]; then
  echo "real Gio frame never exposed new_workspace hit geometry" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

read -r click_x click_y <<<"$hit"
"$WATER_BIN" --socket "$SOCKET" state >"$STATE_BEFORE"
before_count="$(python3 - "$STATE_BEFORE" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
print(len(data.get("workspaces", [])))
PY
)"

"$WATER_BIN" --socket "$SOCKET" ui click   --x "$click_x" --y "$click_y" --click-count 1 >/dev/null

changed=0
for _ in $(seq 1 160); do
  "$WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER"
  after_count="$(python3 - "$STATE_AFTER" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
print(len(data.get("workspaces", [])))
PY
)"
  if (( after_count > before_count )); then
    changed=1
    break
  fi
  sleep 0.025
done
if [[ "$changed" != "1" ]]; then
  echo "real Gio pointer click did not create a workspace" >&2
  cat "$SNAPSHOT" >&2 || true
  cat "$STATE_AFTER" >&2 || true
  cat "$LOG" >&2 || true
  exit 1
fi

"$WATER_BIN" --socket "$SOCKET" ui screenshot --output "$SCREENSHOT" >/dev/null
python3 - "$SCREENSHOT" <<'PY'
import pathlib, sys
data=pathlib.Path(sys.argv[1]).read_bytes()
if len(data) < 8 or data[:8] != b"\x89PNG\r\n\x1a\n":
    raise SystemExit("screenshot is not a PNG")
print(f"real Gio UI smoke passed; screenshot bytes={len(data)}")
PY
