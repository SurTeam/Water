#!/usr/bin/env bash
set -euo pipefail

# Manual native-IME validation for the Go desktop client.
#
# This intentionally uses the real OS input-method path. CI/Xvfb validates the
# deterministic Gio IME state machine, but cannot reliably drive a native IME.
#
# Environment:
#   WATER_BIN             existing Go water binary (optional)
#   WATER_IME_PROBE_TEXT  final text to commit with the native IME
#   WATER_IME_TIMEOUT_MS  wait timeout per phase (default: 120000)
#   WATER_IME_TMP_ROOT    short temporary directory override (optional)
#   WATER_KEEP_IME_LOGS   keep the temporary directory when set to 1

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="${WATER_IME_TMP_ROOT:-/tmp/water-go-ime-manual-$$}"
SOCKET="$TMP_ROOT/water.sock"
CONFIG="$TMP_ROOT/config.json"
LOG="$TMP_ROOT/water-gui.log"
SNAPSHOT="$TMP_ROOT/ui-snapshot.json"
PROBE_TEXT="${WATER_IME_PROBE_TEXT:-输入法测试Water2026}"
TIMEOUT_MS="${WATER_IME_TIMEOUT_MS:-120000}"

[[ -n "$PROBE_TEXT" ]] || {
  echo "error: WATER_IME_PROBE_TEXT must not be empty" >&2
  exit 1
}
[[ "$PROBE_TEXT" != *$'\n'* && "$PROBE_TEXT" != *$'\r'* ]] || {
  echo "error: WATER_IME_PROBE_TEXT must be a single line" >&2
  exit 1
}
[[ "$TIMEOUT_MS" =~ ^[0-9]+$ && "$TIMEOUT_MS" -gt 0 ]] || {
  echo "error: WATER_IME_TIMEOUT_MS must be a positive integer" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin) ;;
  Linux)
    [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]] || {
      echo "error: Linux IME validation requires a real desktop DISPLAY or WAYLAND_DISPLAY" >&2
      exit 1
    }
    ;;
  *)
    echo "error: native IME harness supports macOS and Linux desktops" >&2
    exit 1
    ;;
esac

command -v python3 >/dev/null || {
  echo "error: python3 is required" >&2
  exit 1
}

mkdir -p "$TMP_ROOT"

if [[ -n "${WATER_BIN:-}" ]]; then
  WATER="$WATER_BIN"
else
  mkdir -p "$ROOT/target/go-ime-manual"
  (
    cd "$ROOT"
    go build -o target/go-ime-manual/water ./cmd/water
  )
  WATER="$ROOT/target/go-ime-manual/water"
fi
[[ -x "$WATER" ]] || {
  echo "error: WATER_BIN is not executable: $WATER" >&2
  exit 1
}

gui_pid=""
cleanup() {
  if [[ -S "$SOCKET" ]]; then
    "$WATER" --socket "$SOCKET" server shutdown >/dev/null 2>&1 || true
  fi
  if [[ -n "$gui_pid" ]]; then
    kill "$gui_pid" >/dev/null 2>&1 || true
    wait "$gui_pid" >/dev/null 2>&1 || true
  fi
  if [[ "${WATER_KEEP_IME_LOGS:-0}" == "1" ]]; then
    echo "IME validation files retained at $TMP_ROOT"
  else
    rm -rf "$TMP_ROOT"
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

"$WATER" --control-socket "$SOCKET" --config "$CONFIG" >"$LOG" 2>&1 &
gui_pid=$!

for ((attempt=0; attempt<320; attempt++)); do
  if "$WATER" --socket "$SOCKET" ping >/dev/null 2>&1; then
    break
  fi
  sleep 0.025
done
"$WATER" --socket "$SOCKET" ping >/dev/null || {
  echo "error: Water GUI/server never became ready" >&2
  cat "$LOG" >&2 || true
  exit 1
}

terminal_id=""
for ((attempt=0; attempt<320; attempt++)); do
  if "$WATER" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    terminal_id="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
value=data.get("frame_active_terminal") or ""
if value=="00000000-0000-0000-0000-000000000000":
    value=""
print(value)
PY
)"
    if [[ -n "$terminal_id" ]]; then
      break
    fi
  fi
  sleep 0.025
done
if [[ -z "$terminal_id" ]]; then
  echo "error: could not resolve the active terminal from the rendered Gio frame" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  cat "$LOG" >&2 || true
  exit 1
fi

# Put the shell into a byte-preserving echo target. The line typed through the
# native IME is then visible in the authoritative PTY stream without relying on
# shell command parsing.
"$WATER" --socket "$SOCKET" terminal send \
  --terminal "$terminal_id" \
  --text $'cat\n' >/dev/null
sleep 0.15

cat <<EOF

Native IME validation phase 1/2: preedit isolation

1. Focus the Water terminal window.
2. Switch to the OS input method you want to validate.
3. Begin composing the target text below, but DO NOT commit it yet:

   $PROBE_TEXT

The visible preedit may be phonetic/transliterated text (for example Pinyin or
Kana); it does not need to equal the final committed text. Keep the composition
active. The harness will capture whatever non-empty preedit Gio receives and
verify that those bytes have NOT entered the server-owned PTY stream.

EOF

attempts=$(( (TIMEOUT_MS + 49) / 50 ))
preedit_seen=0
preedit_file="$TMP_ROOT/ime-preedit.txt"

for ((attempt=0; attempt<attempts; attempt++)); do
  if "$WATER" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
    if python3 - "$SNAPSHOT" "$preedit_file" <<'PY'
import json, pathlib, sys
data=json.load(open(sys.argv[1]))
preedit=data.get("ime_preedit","")
if not data.get("ime_composing") or not preedit:
    raise SystemExit(1)
pathlib.Path(sys.argv[2]).write_text(preedit, encoding="utf-8")
PY
    then
      preedit_text="$(cat "$preedit_file")"
      if "$WATER" --socket "$SOCKET" terminal contains \
          --terminal "$terminal_id" \
          --text "$preedit_text" \
          --timeout-ms 50 >/dev/null 2>&1; then
        echo "FAIL: IME preedit leaked into the authoritative PTY before commit" >&2
        echo "leaked preedit: $preedit_text" >&2
        exit 1
      fi
      preedit_seen=1
      break
    fi
  fi
  sleep 0.05
done

if [[ "$preedit_seen" != "1" ]]; then
  echo "FAIL: a non-empty native IME preedit was not observed before timeout" >&2
  cat "$SNAPSHOT" >&2 2>/dev/null || true
  exit 1
fi

cat <<EOF

PASS phase 1: native preedit '$preedit_text' is visible in Gio and absent from the PTY.

Native IME validation phase 2/2: commit delivery

Commit the current composition now. If your IME consumes Enter only to choose
the candidate, press Enter once more so the terminal line is completed.

The harness will verify the exact committed UTF-8 text in the server-owned PTY
stream and then verify that Gio cleared its composition state.

EOF

if "$WATER" --socket "$SOCKET" terminal contains \
    --terminal "$terminal_id" \
    --text "$PROBE_TEXT" \
    --timeout-ms "$TIMEOUT_MS" >/dev/null; then
  cleared=0
  for ((attempt=0; attempt<100; attempt++)); do
    if "$WATER" --socket "$SOCKET" ui snapshot >"$SNAPSHOT" 2>/dev/null; then
      cleared="$(python3 - "$SNAPSHOT" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
print(1 if not data.get("ime_composing") and not data.get("ime_preedit","") else 0)
PY
)"
      if [[ "$cleared" == "1" ]]; then
        break
      fi
    fi
    sleep 0.05
  done

  if [[ "$cleared" != "1" ]]; then
    echo "FAIL: committed text reached the PTY but Gio still reports active composition" >&2
    cat "$SNAPSHOT" >&2 2>/dev/null || true
    exit 1
  fi

  echo "PASS: native IME preedit stayed local, committed text reached terminal $terminal_id, and composition cleared"
  "$WATER" --socket "$SOCKET" terminal send-bytes \
    --terminal "$terminal_id" --hex 03 >/dev/null 2>&1 || true
  exit 0
fi

echo "FAIL: committed IME text was not observed before timeout" >&2
echo "terminal=$terminal_id probe=$PROBE_TEXT" >&2
echo "GUI log: $LOG" >&2
exit 1
