#!/usr/bin/env python3
"""Cold-start a real app via LaunchServices with a leaked legacy daemon marker."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--app", type=Path, default=root / "dist/Water Dev.app")
args = parser.parse_args()
app = args.app.resolve()
water = str(app / "Contents/MacOS/water-dev")
server = str(app / "Contents/MacOS/water-srv-dev")
directory = Path(tempfile.mkdtemp(prefix="water-detached-startup.", dir="/tmp"))
socket = str(directory / "control.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({
    "server": {"detached": True, "detach_on_quit": False},
    "shell": {"program": shell, "args": ["-f"]},
}))


def ctl(*argv):
    reply = subprocess.run([water, "ctl", "--socket", socket, *argv],
                           capture_output=True, text=True, timeout=3)
    if reply.returncode:
        raise RuntimeError(reply.stderr.strip())
    return reply.stdout


def wait(predicate, description, timeout=12):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = predicate()
            if result:
                return result
        except (RuntimeError, subprocess.TimeoutExpired):
            pass
        time.sleep(.05)
    raise RuntimeError(f"Timeout: {description}; artifacts={directory}")


def owned_pids():
    rows = subprocess.run(["ps", "-axo", "pid=,command="], capture_output=True,
                          text=True, check=True).stdout.splitlines()
    return [int(row.split(None, 1)[0]) for row in rows
            if str(config) in row and (water in row or server in row)]


try:
    env = os.environ.copy()
    env["WATER_GO_DAEMON_CHILD"] = "1"
    started = time.monotonic()
    subprocess.run([server, "--config", str(config), "--control-socket", socket,
                    "--daemonize"], env=env, capture_output=True, check=True, timeout=3)
    info = wait(lambda: json.loads(ctl("server", "info")), "standalone daemon ready")
    print(f"PASS daemon launcher returned in {time.monotonic()-started:.3f}s; "
          f"owned_server_pid={info['server_pid']}", flush=True)
    ctl("server", "shutdown")
    wait(lambda: not owned_pids(), "first daemon exited")

    started = time.monotonic()
    subprocess.run(["open", "-n", str(app), "--env", "WATER_GO_DAEMON_CHILD=1",
                    "--stderr", str(directory / "gui.log"), "--args",
                    "--config", str(config), "--control-socket", socket], check=True)

    def ready():
        state = json.loads(ctl("ui", "snapshot"))
        if (state.get("terminal_grids") and state.get("native_menu", {}).get("ready")
                and not state.get("application_hidden")
                and not state.get("window_minimized")):
            return state

    state = wait(ready, "cold LaunchServices GUI displayed")
    info = json.loads(ctl("server", "info"))
    print(f"PASS open cold-start in {time.monotonic()-started:.3f}s; "
          f"owned_gui_pid={state['gui_pid']} owned_server_pid={info['server_pid']}", flush=True)
    pane, terminal = state["frame_focused_pane"], state["frame_active_terminal"]
    ctl("pane", "input", "--pane", pane, "--text",
        "printf 'DAEMON_ENV=%s\\n' \"${WATER_GO_DAEMON_CHILD-unset}\"\n")
    ctl("terminal", "contains", "--terminal", terminal, "DAEMON_ENV=unset",
        "--timeout-ms", "5000")
    ctl("ui", "screenshot", "--output", str(directory / "cold-start.png"))
    print(f"PASS daemon marker absent in shell; artifacts={directory}", flush=True)
    ctl("ui", "menu", "quit-and-server")
    wait(lambda: not owned_pids(), "test GUI/server exited")
finally:
    # Match both the unique owned config and the exact bundle executable.
    for pid in owned_pids():
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
