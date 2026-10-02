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

# Exercise the real X11 clipboard and keyboard path rather than the internal
# automation helpers. Focus the visible terminal pane, put a shell command in
# the clipboard, press Ctrl+V, then Return, and verify the PTY output.
pane_hit=""
terminal_id=""
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null &&
     "$WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER" 2>/dev/null; then
    pane_hit="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
for hit in data.get("automation_hits", []):
    if hit.get("kind")=="pane":
        x0,y0,x1,y1=hit["rect"]
        print(f"{(x0+x1)/2:.1f} {(y0+y1)/2:.1f}")
        break
PY
)"
    terminal_id="$(python3 - "$STATE_AFTER" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))

def find_terminal(value):
    if isinstance(value, dict):
        terminal=value.get("terminal")
        if isinstance(terminal, dict):
            summary=terminal.get("summary")
            if isinstance(summary, dict) and summary.get("terminal_id"):
                return summary["terminal_id"]
        if value.get("terminal_id"):
            return value["terminal_id"]
        for child in value.values():
            found=find_terminal(child)
            if found:
                return found
    elif isinstance(value, list):
        for child in value:
            found=find_terminal(child)
            if found:
                return found
    return None

root=data.get("workspace") or data
print(find_terminal(root) or "")
PY
)"
    if [[ -n "$pane_hit" && -n "$terminal_id" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ -z "$pane_hit" || -z "$terminal_id" ]]; then
  echo "could not resolve active terminal pane for clipboard smoke" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$STATE_AFTER" >&2 2>/dev/null || true
  exit 1
fi

window_id="$(xdotool search --onlyvisible --name 'Water' 2>/dev/null | head -n1 || true)"
if [[ -z "$window_id" ]]; then
  echo "could not find visible Water X11 window" >&2
  exit 1
fi
read -r pane_x pane_y <<<"$pane_hit"
xdotool windowfocus --sync "$window_id"
xdotool mousemove --window "$window_id" "$pane_x" "$pane_y" click 1
sleep 0.15

# First prove that the real X11 keyboard/focus path reaches Gio and the PTY.
xdotool type --delay 3 'echo WATER_X11_KEY_SMOKE'
xdotool key Return
if ! "$WATER_BIN" --socket "$SOCKET" terminal contains \
    --terminal "$terminal_id" \
    --text WATER_X11_KEY_SMOKE \
    --timeout-ms 5000 >/dev/null; then
  echo "real X11 keyboard input did not reach the PTY" >&2
  echo "window_id=$window_id focused=$(xdotool getwindowfocus 2>/dev/null || true) pane=$pane_hit terminal=$terminal_id" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$STATE_AFTER" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

# Then isolate the clipboard path. Verify the X selection itself before asking
# Gio to read it, and send the shortcut to the already-focused window.
clipboard_text='echo WATER_CLIPBOARD_SMOKE'
printf '%s' "$clipboard_text" | xclip -selection clipboard
sleep 0.1
clipboard_readback="$(xclip -selection clipboard -o 2>/dev/null || true)"
if [[ "$clipboard_readback" != "$clipboard_text" ]]; then
  echo "X11 clipboard self-check failed: got '$clipboard_readback'" >&2
  exit 1
fi

xdotool key ctrl+v
sleep 0.15
xdotool key Return

if ! "$WATER_BIN" --socket "$SOCKET" terminal contains \
    --terminal "$terminal_id" \
    --text WATER_CLIPBOARD_SMOKE \
    --timeout-ms 5000 >/dev/null; then
  echo "real Gio clipboard paste did not reach the PTY after keyboard path succeeded" >&2
  echo "window_id=$window_id focused=$(xdotool getwindowfocus 2>/dev/null || true) pane=$pane_hit terminal=$terminal_id" >&2
  cat "$LOG" >&2 || true
  exit 1
fi

remote_hit=""
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    remote_hit="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
for hit in data.get("automation_hits", []):
    if hit.get("kind")=="new_remote":
        x0,y0,x1,y1=hit["rect"]
        print(f"{(x0+x1)/2:.1f} {(y0+y1)/2:.1f}")
        break
PY
)"
    if [[ -n "$remote_hit" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ -z "$remote_hit" ]]; then
  echo "real Gio frame never exposed new_remote hit geometry" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

read -r remote_x remote_y <<<"$remote_hit"
"$WATER_BIN" --socket "$SOCKET" ui click --x "$remote_x" --y "$remote_y" --click-count 1 >/dev/null

remote_form=0
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    remote_form="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
print(1 if data.get("remote_form_visible") else 0)
PY
)"
    if [[ "$remote_form" == "1" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ "$remote_form" != "1" ]]; then
  echo "real Gio pointer click did not open the runtime remote form" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
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
