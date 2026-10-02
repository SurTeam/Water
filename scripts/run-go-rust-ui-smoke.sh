#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WATER_BIN="${WATER_BIN:-$ROOT/target/go-ui-smoke/water}"
RUST_SERVER_BIN="${WATER_RUST_SERVER_BIN:-$ROOT/target/debug/water-server}"
TMP_ROOT="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/water-go-rust-ui-smoke-$$"
SOCKET="$TMP_ROOT/water.sock"
CONFIG="$TMP_ROOT/config.json"
SERVER_LOG="$TMP_ROOT/rust-server.log"
GUI_LOG="$TMP_ROOT/go-gui.log"
SNAPSHOT="$TMP_ROOT/ui-snapshot.json"
STATE_BEFORE="$TMP_ROOT/state-before.json"
STATE_AFTER="$TMP_ROOT/state-after.json"
SCREENSHOT="$TMP_ROOT/go-rust-water.png"
mkdir -p "$TMP_ROOT"

server_pid=""
gui_pid=""
cleanup() {
  if [[ -S "$SOCKET" ]]; then
    "$WATER_BIN" --socket "$SOCKET" server shutdown >/dev/null 2>&1 || true
  fi
  if [[ -n "$gui_pid" ]]; then
    kill "$gui_pid" >/dev/null 2>&1 || true
    wait "$gui_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  if [[ "${WATER_KEEP_UI_SMOKE:-0}" != "1" ]]; then
    rm -rf "$TMP_ROOT"
  else
    echo "cross-language UI smoke files retained at $TMP_ROOT"
  fi
}
trap cleanup EXIT INT TERM

if [[ ! -x "$WATER_BIN" ]]; then
  echo "missing Go Water binary: $WATER_BIN" >&2
  exit 1
fi
if [[ ! -x "$RUST_SERVER_BIN" ]]; then
  echo "missing Rust water-server binary: $RUST_SERVER_BIN" >&2
  exit 1
fi

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
    "auto_start": false,
    "detached": false,
    "detach_on_quit": false
  },
  "shell": {
    "program": "/bin/sh",
    "args": ["-l"]
  }
}
JSON

"$RUST_SERVER_BIN" --control-socket "$SOCKET" --config "$CONFIG" >"$SERVER_LOG" 2>&1 &
server_pid=$!

for _ in $(seq 1 320); do
  if "$WATER_BIN" --socket "$SOCKET" ping >/dev/null 2>&1; then
    break
  fi
  sleep 0.025
done
if ! "$WATER_BIN" --socket "$SOCKET" ping >/dev/null 2>&1; then
  echo "Rust Water server never became ready" >&2
  cat "$SERVER_LOG" >&2 || true
  exit 1
fi

"$WATER_BIN" --control-socket "$SOCKET" --config "$CONFIG" >"$GUI_LOG" 2>&1 &
gui_pid=$!

find_hit() {
  local kind="$1"
  python3 - "$SNAPSHOT" "$kind" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
kind=sys.argv[2]
for hit in data.get("automation_hits", []):
    if hit.get("kind")==kind:
        x0,y0,x1,y1=hit["rect"]
        print(f"{(x0+x1)/2:.1f} {(y0+y1)/2:.1f}")
        break
PY
}

workspace_hit=""
for _ in $(seq 1 320); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    workspace_hit="$(find_hit new_workspace)"
    if [[ -n "$workspace_hit" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ -z "$workspace_hit" ]]; then
  echo "Go GUI on Rust server never exposed real workspace hit geometry" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$GUI_LOG" >&2 || true
  cat "$SERVER_LOG" >&2 || true
  exit 1
fi

"$WATER_BIN" --socket "$SOCKET" state >"$STATE_BEFORE"
before_count="$(python3 - "$STATE_BEFORE" <<'PY'
import json, sys
print(len(json.load(open(sys.argv[1])).get("workspaces", [])))
PY
)"
read -r click_x click_y <<<"$workspace_hit"
"$WATER_BIN" --socket "$SOCKET" ui click --x "$click_x" --y "$click_y" --click-count 1 >/dev/null

changed=0
for _ in $(seq 1 200); do
  "$WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER"
  after_count="$(python3 - "$STATE_AFTER" <<'PY'
import json, sys
print(len(json.load(open(sys.argv[1])).get("workspaces", [])))
PY
)"
  if (( after_count > before_count )); then
    changed=1
    break
  fi
  sleep 0.025
done
if [[ "$changed" != "1" ]]; then
  echo "Go GUI pointer click did not mutate Rust server state" >&2
  cat "$STATE_AFTER" >&2 || true
  cat "$GUI_LOG" >&2 || true
  cat "$SERVER_LOG" >&2 || true
  exit 1
fi

remote_hit=""
for _ in $(seq 1 200); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    remote_hit="$(find_hit new_remote)"
    if [[ -n "$remote_hit" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ -z "$remote_hit" ]]; then
  echo "Go GUI on Rust server never exposed runtime Remote hit geometry" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  exit 1
fi

read -r remote_x remote_y <<<"$remote_hit"
"$WATER_BIN" --socket "$SOCKET" ui click --x "$remote_x" --y "$remote_y" --click-count 1 >/dev/null
remote_form=0
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    remote_form="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
print(1 if json.load(open(sys.argv[1])).get("remote_form_visible") else 0)
PY
)"
    if [[ "$remote_form" == "1" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ "$remote_form" != "1" ]]; then
  echo "runtime Remote form did not open over Rust server" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  exit 1
fi

"$WATER_BIN" --socket "$SOCKET" ui screenshot --output "$SCREENSHOT" >/dev/null
python3 - "$SCREENSHOT" <<'PY'
import pathlib, sys
data=pathlib.Path(sys.argv[1]).read_bytes()
png_magic=bytes.fromhex("89504e470d0a1a0a")
if len(data) < len(png_magic) or data[:len(png_magic)] != png_magic:
    raise SystemExit("cross-language screenshot is not a PNG")
print(f"Go GUI ↔ Rust server real-window smoke passed; screenshot bytes={len(data)}")
PY
