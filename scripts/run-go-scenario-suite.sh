#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WATER_BIN="${WATER_BIN:-$ROOT/target/go-scenarios/water}"
WATER_SERVER_BIN="${WATER_SERVER_BIN:-$ROOT/target/go-scenarios/water-server}"
REQUIRE_ALL="${WATER_SCENARIO_REQUIRE_ALL:-0}"
TMP_ROOT="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/water-go-scenarios-$$"
mkdir -p "$TMP_ROOT"

current_pid=""
current_socket=""
cleanup() {
  if [[ -n "$current_socket" && -S "$current_socket" ]]; then
    "$WATER_BIN" --socket "$current_socket" server shutdown >/dev/null 2>&1 || true
  fi
  if [[ -n "$current_pid" ]]; then
    kill "$current_pid" >/dev/null 2>&1 || true
    wait "$current_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT INT TERM

if [[ ! -x "$WATER_BIN" ]]; then
  echo "missing WATER_BIN: $WATER_BIN" >&2
  exit 1
fi
if [[ ! -x "$WATER_SERVER_BIN" ]]; then
  echo "missing WATER_SERVER_BIN: $WATER_SERVER_BIN" >&2
  exit 1
fi

config="$TMP_ROOT/config.json"
cat >"$config" <<'JSON'
{
  "startup": {
    "initial_workspace": false,
    "initial_terminal": false
  },
  "shell": {
    "program": "/bin/sh",
    "args": ["-l"]
  },
  "server": {
    "detached": false,
    "auto_start": false,
    "detach_on_quit": false
  }
}
JSON

wait_ready() {
  local socket="$1"
  local log="$2"
  for _ in $(seq 1 200); do
    if "$WATER_BIN" --socket "$socket" ping >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.025
  done
  echo "Go scenario server did not become ready" >&2
  cat "$log" >&2 || true
  return 1
}

run_one() {
  local path="$1"
  local name
  name="$(basename "$path" .json)"
  local socket="$TMP_ROOT/$name.sock"
  local log="$TMP_ROOT/$name.server.log"

  rm -f "$socket"
  "$WATER_SERVER_BIN"     --control-socket "$socket"     --config "$config"     --empty-workspace     >"$log" 2>&1 &
  current_pid=$!
  current_socket="$socket"

  if ! wait_ready "$socket" "$log"; then
    return 1
  fi

  echo "==> Go scenario: $name"
  if ! "$WATER_BIN" --socket "$socket" scenario run "$path"; then
    echo "--- water-server log: $name ---" >&2
    cat "$log" >&2 || true
    return 1
  fi

  "$WATER_BIN" --socket "$socket" server shutdown >/dev/null 2>&1 || true
  wait "$current_pid" || {
    status=$?
    echo "water-server exited with status $status for $name" >&2
    cat "$log" >&2 || true
    return "$status"
  }
  current_pid=""
  current_socket=""
  rm -f "$socket"
}

require_executable() {
  local executable="$1"
  local scenario="$2"
  if [[ -x "$executable" ]]; then
    return 0
  fi
  if [[ "$REQUIRE_ALL" == "1" ]]; then
    echo "required scenario dependency is missing: $executable ($scenario)" >&2
    exit 1
  fi
  echo "==> Skipping $(basename "$scenario"): missing $executable"
  return 1
}

run_one "$ROOT/tests/scenarios/workspace_basic.json"
run_one "$ROOT/tests/scenarios/terminal_basic.json"

agent="$ROOT/tests/scenarios/agent_detection.json"
if require_executable /bin/zsh "$agent"; then
  run_one "$agent"
fi

zsh="$ROOT/tests/scenarios/terminal_zsh.json"
if require_executable /opt/homebrew/bin/zsh "$zsh"; then
  run_one "$zsh"
fi
