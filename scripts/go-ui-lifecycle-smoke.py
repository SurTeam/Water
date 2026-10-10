#!/usr/bin/env python3
"""Verify native menu actions and an owned detached server's lifetime."""
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import tempfile
import time

os.environ["WATER_TEST_UNFOCUSED"] = "1"

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
directory = Path(tempfile.mkdtemp(prefix="water-lifetime.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"startup": {"window_columns": 96, "window_rows": 30},
    "server": {"detached": True, "detach_on_quit": True}, "shell": {"program": shell, "args": ["-f"]},
    "shortcuts": {"focus_left": "cmd-h", "hide_window": "cmd-w", "minimize_window": "cmd-m", "ignore_quit": "cmd-q"}}))
gui = None
server_pid = None
child_gui_pid = None
logs = []

def start():
    global gui
    log = (directory / f"gui-{len(logs)}.log").open("w")
    logs.append(log)
    gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], stdout=log, stderr=subprocess.STDOUT)
    print(f"owned_gui_pid={gui.pid} socket={socket}", flush=True)

def ctl(*args):
    result = subprocess.run([water, "ctl", "--socket", socket, *args], capture_output=True, timeout=6)
    if result.returncode:
        raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout)

def wait(predicate, description):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError("GUI exited while waiting for " + description)
        try:
            state = ctl("ui", "snapshot")
            if predicate(state):
                return state
        except RuntimeError:
            pass
        time.sleep(.025)
    raise RuntimeError("Timeout: " + description + " artifacts=" + str(directory))

try:
    start()
    initial = wait(lambda s: s.get("native_menu", {}).get("ready") and s.get("frame_active_terminal") not in (None, "00000000-0000-0000-0000-000000000000"), "native menu and terminal")
    server_pid = ctl("server", "info")["server_pid"]
    print(f"owned_server_pid={server_pid}", flush=True)
    assert server_pid != gui.pid
    assert ctl("ui", "key", "cmd-n")["handled"]
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        inventory = subprocess.check_output(["ps", "-axo", "pid=,ppid=,command="], text=True)
        for line in inventory.splitlines():
            fields = line.strip().split(None, 2)
            if len(fields) == 3 and int(fields[1]) == gui.pid and fields[2].startswith(water+" ") and socket in fields[2]:
                child_gui_pid = int(fields[0])
                break
        if child_gui_pid is not None and ctl("server", "info")["ui_sessions"] == 2:
            break
        time.sleep(.025)
    else:
        raise RuntimeError("Cmd+N did not create a second native GUI attached to the owned server")
    print(f"owned_child_gui_pid={child_gui_pid}", flush=True)
    second = wait(lambda s: s.get("gui_pid") == child_gui_pid, "second native window")
    assert second["window_size"] == initial["window_size"]
    # Clean only the child whose parent, executable and unique socket match.
    os.kill(child_gui_pid, signal.SIGTERM)
    wait(lambda s: ctl("server", "info")["ui_sessions"] == 1, "child GUI detached")
    child_gui_pid = None
    if platform.system() == "Darwin":
        items = initial["native_menu"]["items"]
        assert items["hide-window"]["key"] == "w"
        assert items["quit-gui"]["key"] == "" and items["quit-and-server"]["enabled"]
    left = initial["frame_focused_pane"]
    assert ctl("ui", "key", "cmd-\\")["handled"]
    wait(lambda s: len([h for h in s["automation_hits"] if h["kind"] == "pane"]) == 2 and s["frame_focused_pane"] != left, "split pane")
    assert ctl("ui", "key", "cmd-h")["handled"]
    focused = wait(lambda s: s["frame_focused_pane"] == left, "Cmd+H focuses left pane")
    assert not focused["application_hidden"]
    assert ctl("ui", "key", "cmd-q")["handled"]
    assert ctl("ui", "snapshot")["frame_focused_pane"] == left
    assert ctl("server", "info")["server_pid"] == server_pid
    assert ctl("ui", "key", "cmd-w")["handled"]
    wait(lambda s: s["application_hidden"], "Cmd+W hides application")
    assert ctl("server", "info")["server_pid"] == server_pid
    ctl("ui", "menu", "show-window")
    wait(lambda s: not s["application_hidden"], "native Show Water menu")
    assert ctl("ui", "key", "cmd-m")["handled"]
    wait(lambda s: s["window_minimized"], "configured minimize")
    ctl("ui", "menu", "show-window")
    wait(lambda s: not s["window_minimized"] and not s["application_hidden"], "native restore")
    ctl("ui", "menu", "quit-gui")
    assert gui.wait(timeout=5) == 0
    assert ctl("server", "info")["server_pid"] == server_pid, "GUI-only quit should preserve detached server"
    start()
    wait(lambda s: s.get("native_menu", {}).get("ready"), "reattached native menu")
    assert ctl("server", "info")["server_pid"] == server_pid
    ctl("ui", "menu", "quit-and-server")
    assert gui.wait(timeout=8) == 0
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        try:
            os.kill(server_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(.025)
    else:
        raise RuntimeError("Owned server still running after combined quit")
    assert not Path(socket).exists(), "combined quit must remove owned socket"
    print("PASS Cmd+H pane focus, Cmd+W hide, Cmd+Q ignore, minimize/restore; actual native menu GUI-only quit and GUI+server quit; detached server process and socket removed", flush=True)
finally:
    # Every operation is scoped to this instance's unique socket and recorded PIDs.
    try:
        if server_pid is not None and ctl("server", "info")["server_pid"] == server_pid:
            ctl("server", "shutdown")
    except Exception:
        pass
    if child_gui_pid is not None:
        try:
            os.kill(child_gui_pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    if gui is not None and gui.poll() is None:
        gui.terminate()
        try:
            gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gui.kill()
            gui.wait(timeout=5)
    for log in logs:
        log.close()
