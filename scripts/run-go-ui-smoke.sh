#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WATER_BIN="${WATER_BIN:-$ROOT/target/go-ui-smoke/water}"
PYTHON="${WATER_PYTHON:-$HOME/.venv/bin/python}"
[[ -x "$PYTHON" ]] || PYTHON="$(command -v python3)"
TEST_SHELL="$(command -v zsh || true)"
[[ ! -x /opt/homebrew/bin/zsh ]] || TEST_SHELL=/opt/homebrew/bin/zsh
[[ -n "$TEST_SHELL" ]] || { echo "error: zsh is required for the GUI smoke" >&2; exit 1; }
case "$(uname -s)" in
  Darwin) ;;
  Linux)
    [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]] || {
      echo "error: GUI smoke requires DISPLAY or WAYLAND_DISPLAY" >&2
      exit 1
    }
    ;;
  *) echo "error: GUI smoke supports macOS and Linux" >&2; exit 1 ;;
esac
# Keep control socket paths below Darwin's Unix socket path limit.
TMP_ROOT="$(mktemp -d /tmp/water-go-ui-smoke.XXXXXX)"
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

cat >"$CONFIG" <<JSON
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
    "program": "$TEST_SHELL",
    "args": ["-f"]
  }
}
JSON

"$WATER_BIN" --control-socket "$SOCKET" --config "$CONFIG" >"$LOG" 2>&1 &
gui_pid=$!

for _ in $(seq 1 240); do
  if ! kill -0 "$gui_pid" 2>/dev/null; then
    echo "Water GUI exited before control readiness" >&2
    cat "$LOG" >&2 || true
    exit 1
  fi
  if "$WATER_BIN" --socket "$SOCKET" ping >/dev/null 2>&1; then
    break
  fi
  sleep 0.025
done
"$WATER_BIN" --socket "$SOCKET" ping >/dev/null

# Wait for a real Ebitengine frame to populate actual clickable geometry.
hit=""
for _ in $(seq 1 240); do
  if ! kill -0 "$gui_pid" 2>/dev/null; then
    echo "Water GUI exited before its first frame" >&2
    cat "$LOG" >&2 || true
    exit 1
  fi
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    hit="$("$PYTHON" - "$SNAPSHOT" <<'PY'
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
  echo "real Ebitengine frame never exposed new_workspace hit geometry" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

read -r click_x click_y <<<"$hit"
"$WATER_BIN" --socket "$SOCKET" state >"$STATE_BEFORE"
before_count="$("$PYTHON" - "$STATE_BEFORE" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
print(len(data.get("workspaces", [])))
PY
)"

"$WATER_BIN" --socket "$SOCKET" ui click   --x "$click_x" --y "$click_y" --click-count 1 >/dev/null

changed=0
for _ in $(seq 1 160); do
  "$WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER"
  after_count="$("$PYTHON" - "$STATE_AFTER" <<'PY'
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
  echo "real Ebitengine pointer click did not create a workspace" >&2
  cat "$SNAPSHOT" >&2 || true
  cat "$STATE_AFTER" >&2 || true
  cat "$LOG" >&2 || true
  exit 1
fi

# Resolve the terminal from the same rendered revision as its hit geometry.
pane_hit=""
terminal_id=""
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null &&
     "$WATER_BIN" --socket "$SOCKET" state >"$STATE_AFTER" 2>/dev/null; then
    pane_info="$("$PYTHON" - "$SNAPSHOT" "$STATE_AFTER" <<'PY'
import json, sys
snapshot=json.load(open(sys.argv[1]))
state=json.load(open(sys.argv[2]))

# Hit rectangles are meaningful only for the exact model revision that was
# rendered into that frame. Wait until the public state endpoint catches the
# same revision instead of pairing stale geometry with a newer workspace.
if snapshot.get("frame_state_revision") != state.get("state_revision"):
    raise SystemExit(0)
if snapshot.get("frame_active_workspace") != state.get("active_workspace"):
    raise SystemExit(0)
if snapshot.get("frame_focused_pane") != state.get("focused_pane"):
    raise SystemExit(0)

pane_id=snapshot.get("frame_focused_pane") or ""
terminal_id=snapshot.get("frame_active_terminal") or ""
nil="00000000-0000-0000-0000-000000000000"
if not pane_id or not terminal_id or pane_id==nil or terminal_id==nil:
    raise SystemExit(0)

for hit in snapshot.get("automation_hits", []):
    if hit.get("kind")=="pane" and hit.get("id")==pane_id:
        x0,y0,x1,y1=hit["rect"]
        print(pane_id, f"{(x0+x1)/2:.1f}", f"{(y0+y1)/2:.1f}", terminal_id)
        break
PY
)"
    if [[ -n "$pane_info" ]]; then
      read -r pane_id pane_x pane_y terminal_id <<<"$pane_info"
      pane_hit="$pane_x $pane_y"
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

read -r pane_x pane_y <<<"$pane_hit"
"$WATER_BIN" --socket "$SOCKET" ui click --x "$pane_x" --y "$pane_y" >/dev/null

# Control input goes through the native Ebitengine handler and dispatcher.
# This validates client/server delivery; native clipboard/IME are manual gates.
marker="WATER_Go_KEY_SMOKE_$$"
"$WATER_BIN" --socket "$SOCKET" ui key "text:printf 'WATER_Go_%s\\n' 'KEY_SMOKE_$$'" >/dev/null
"$WATER_BIN" --socket "$SOCKET" ui key Return >/dev/null
if ! "$WATER_BIN" --socket "$SOCKET" terminal contains \
    --terminal "$terminal_id" \
    --text "$marker" \
    --timeout-ms 5000 >/dev/null; then
  echo "Ebitengine control keyboard input did not reach the PTY" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$STATE_AFTER" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

# The notification controls must be present in the rendered Settings model.
"$WATER_BIN" --socket "$SOCKET" ui key cmd-, >/dev/null
settings_visible=0
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    settings_visible="$($PYTHON - "$SNAPSHOT" <<'PY'
import json, sys
settings=json.load(open(sys.argv[1]))
required={"UI.SystemNotifications", "UI.AgentLongRunNotificationSeconds", "UI.ShellLongRunNotificationSeconds"}
fields={field["name"] for field in settings.get("settings_fields", [])}
print(1 if settings.get("settings_visible") and required <= fields else 0)
PY
)"
    if [[ "$settings_visible" == "1" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ "$settings_visible" != "1" ]]; then
  echo "notification controls were not exposed in Settings" >&2
  cat "$SNAPSHOT" >&2 || true
  cat "$LOG" >&2 || true
  exit 1
fi
"$WATER_BIN" --socket "$SOCKET" ui key cmd-, >/dev/null
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null &&
     [[ "$("$PYTHON" -c 'import json,sys; print(int(json.load(open(sys.argv[1])).get("settings_visible", False)))' "$SNAPSHOT")" == "0" ]]; then
    break
  fi
  sleep 0.025
done

remote_hit=""
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    remote_hit="$("$PYTHON" - "$SNAPSHOT" <<'PY'
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
  echo "real Ebitengine frame never exposed new_remote hit geometry" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

read -r remote_x remote_y <<<"$remote_hit"
"$WATER_BIN" --socket "$SOCKET" ui click --x "$remote_x" --y "$remote_y" --click-count 1 >/dev/null

remote_form=0
for _ in $(seq 1 160); do
  if "$WATER_BIN" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    remote_form="$("$PYTHON" - "$SNAPSHOT" <<'PY'
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
  echo "real Ebitengine pointer click did not open the runtime remote form" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

"$WATER_BIN" --socket "$SOCKET" ui screenshot --output "$SCREENSHOT" >/dev/null
"$PYTHON" - "$SCREENSHOT" <<'PY'
import pathlib, sys
data=pathlib.Path(sys.argv[1]).read_bytes()
if len(data) < 8 or data[:8] != b"\x89PNG\r\n\x1a\n":
    raise SystemExit("screenshot is not a PNG")
print(f"real Ebitengine UI smoke passed; screenshot bytes={len(data)}")
PY
