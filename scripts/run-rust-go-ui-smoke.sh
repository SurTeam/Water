#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_WATER_BIN="${GO_WATER_BIN:-$ROOT/target/go-ui-smoke/water}"
GO_SERVER_BIN="${GO_SERVER_BIN:-$ROOT/target/go-ui-smoke/water-server}"
RUST_GUI_BIN="${WATER_RUST_GUI_BIN:-$ROOT/target/debug/water}"
TMP_ROOT="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/water-rust-go-ui-smoke-$$"
SOCKET="$TMP_ROOT/water.sock"
CONFIG="$TMP_ROOT/config.json"
SERVER_LOG="$TMP_ROOT/go-server.log"
GUI_LOG="$TMP_ROOT/rust-gui.log"
SNAPSHOT="$TMP_ROOT/ui-snapshot.json"
STATE_BEFORE="$TMP_ROOT/state-before.json"
STATE_AFTER="$TMP_ROOT/state-after.json"
SCREENSHOT="$TMP_ROOT/rust-go-water.png"
mkdir -p "$TMP_ROOT"

server_pid=""
gui_pid=""
cleanup() {
  if [[ -S "$SOCKET" ]]; then
    "$GO_WATER_BIN" --socket "$SOCKET" server shutdown >/dev/null 2>&1 || true
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
    echo "reverse cross-language UI smoke files retained at $TMP_ROOT"
  fi
}
trap cleanup EXIT INT TERM

for binary in "$GO_WATER_BIN" "$GO_SERVER_BIN" "$RUST_GUI_BIN"; do
  if [[ ! -x "$binary" ]]; then
    echo "missing executable: $binary" >&2
    exit 1
  fi
done

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

"$GO_SERVER_BIN" --control-socket "$SOCKET" --config "$CONFIG" >"$SERVER_LOG" 2>&1 &
server_pid=$!

for _ in $(seq 1 320); do
  if "$GO_WATER_BIN" --socket "$SOCKET" ping >/dev/null 2>&1; then
    break
  fi
  sleep 0.025
done
if ! "$GO_WATER_BIN" --socket "$SOCKET" ping >/dev/null 2>&1; then
  echo "Go Water server never became ready" >&2
  cat "$SERVER_LOG" >&2 || true
  exit 1
fi

"$RUST_GUI_BIN" --control-socket "$SOCKET" --config "$CONFIG" >"$GUI_LOG" 2>&1 &
gui_pid=$!

for _ in $(seq 1 400); do
  if "$GO_WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    if python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
raise SystemExit(0 if data.get("has_active_window") and data.get("window_count",0) >= 1 else 1)
PY
    then
      break
    fi
  fi
  sleep 0.025
done
if ! "$GO_WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
  echo "Rust GUI never registered with Go server UI forwarding" >&2
  cat "$GUI_LOG" >&2 || true
  cat "$SERVER_LOG" >&2 || true
  exit 1
fi

"$GO_WATER_BIN" --socket "$SOCKET" state >"$STATE_BEFORE"
before_count="$(python3 - "$STATE_BEFORE" <<'PY'
import json, sys
print(len(json.load(open(sys.argv[1])).get("workspaces", [])))
PY
)"

# Rust UI automation intentionally accepts absolute coordinates. The new
# workspace button lives in the left sidebar; use the same stable click point
# covered by the Rust UI control path rather than Go-only automation geometry.
"$GO_WATER_BIN" --socket "$SOCKET" ui click --x 92 --y 112 --click-count 1 >/dev/null

changed=0
for _ in $(seq 1 200); do
  "$GO_WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER"
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
  # Coordinate layouts can evolve independently; fall back to the Rust
  # keystroke automation path to still validate GUI->Go command dispatch.
  "$GO_WATER_BIN" --socket "$SOCKET" ui key command-shift-n >/dev/null
  for _ in $(seq 1 200); do
    "$GO_WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER"
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
fi

if [[ "$changed" != "1" ]]; then
  echo "Rust GUI automation did not mutate Go server state" >&2
  cat "$STATE_AFTER" >&2 || true
  cat "$GUI_LOG" >&2 || true
  cat "$SERVER_LOG" >&2 || true
  exit 1
fi

"$GO_WATER_BIN" --socket "$SOCKET" ui screenshot --output "$SCREENSHOT" >/dev/null
python3 - "$SCREENSHOT" <<'PY'
import pathlib, sys
data=pathlib.Path(sys.argv[1]).read_bytes()
png_magic=bytes.fromhex("89504e470d0a1a0a")
if len(data) < len(png_magic) or data[:len(png_magic)] != png_magic:
    raise SystemExit("reverse cross-language screenshot is not a PNG")
print(f"Rust GUI ↔ Go server real-window smoke passed; screenshot bytes={len(data)}")
PY
