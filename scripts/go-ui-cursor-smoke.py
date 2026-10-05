#!/usr/bin/env python3
"""Verify cursor preferences and IME geometry through an owned native Water GUI."""
import json
import os
from pathlib import Path
import platform
import shlex
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
if platform.system() == "Linux" and not (os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY")):
    raise RuntimeError("Native display required")
directory = Path(tempfile.mkdtemp(prefix="water-cursor.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"server": {"detached": False, "detach_on_quit": False},
    "shell": {"program": shell, "args": ["-f"]}, "startup": {"window_columns": 96, "window_rows": 30}}))
log = (directory / "gui.log").open("w")
gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], stdout=log, stderr=subprocess.STDOUT)
print(f"owned_gui_pid={gui.pid} socket={socket}", flush=True)

def ctl(*args):
    result = subprocess.run([water, "ctl", "--socket", socket, *args], capture_output=True, timeout=5)
    if result.returncode:
        raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout)

def wait(predicate, description):
    deadline = time.monotonic() + 8
    state = {}
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError("GUI exited: " + (directory / "gui.log").read_text())
        try:
            state = ctl("ui", "state")
            if predicate(state):
                return state
        except RuntimeError:
            pass
        time.sleep(.025)
    (directory / "failure.json").write_text(json.dumps(state, indent=2))
    raise RuntimeError("Timeout: " + description + " artifacts=" + str(directory))

def click(label):
    state = wait(lambda s: any(h.get("label") == label for h in s["automation_hits"]), label)
    hit = next(h for h in state["automation_hits"] if h.get("label") == label)
    x0, y0, x1, y1 = hit["rect"]
    assert ctl("ui", "click", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2))["handled"]

def grid(state):
    return next(g for g in state["terminal_grids"] if g["pane_id"] == state["frame_focused_pane"])

try:
    initial = wait(lambda s: bool(s.get("terminal_grids")) and s.get("window_focused"), "focused native terminal")
    pane = initial["frame_focused_pane"]
    # Reset any shell DECSCUSR override before checking user defaults.
    ctl("pane", "input", "--pane", pane, "--text", "printf '\\033[0 q'\n")
    results = []
    for style in ("block", "bar", "underline"):
        for blink in (True, False):
            ctl("ui", "key", "cmd-," if platform.system() == "Darwin" else "ctrl-,")
            wait(lambda s: s.get("settings_visible"), "settings")
            click("choice:CursorStyle:" + style)
            current = ctl("ui", "state")["effective_config"]["terminal"]["cursor_blink"]
            if current != blink:
                click("field:Terminal.CursorBlink")
            click("save")
            wait(lambda s: s.get("settings_message") == "Settings saved", "settings saved")
            click("cancel")
            state = wait(lambda s: not s.get("settings_visible") and
                grid(s)["cursor_style"] == style and grid(s)["cursor_blink"] == blink, "runtime cursor")
            saved = json.loads(config.read_text())
            saved = saved.get("overrides", saved)["terminal"]
            assert saved["cursor_style"] == style and saved["cursor_blink"] == blink
            ctl("ui", "screenshot", "--output", str(directory / f"{style}-{blink}.png"))
            results.append({"style": style, "blink": blink})
    # A raw PTY keeps the cursor at a known position while the GUI remains idle.
    probe = """import os, termios, tty
old = termios.tcgetattr(0)
try:
    tty.setraw(0)
    os.write(1, b'\\x1b[0 q\\x1b[2J\\x1b[5;11H')
    os.read(0, 1)
    os.write(1, b'\\x1b[20;31H')
    os.read(0, 1)
finally:
    termios.tcsetattr(0, termios.TCSANOW, old)
"""
    python = str(Path.home() / ".venv/bin/python")
    ctl("pane", "input", "--pane", pane, "--text", python + " -c " + shlex.quote(probe) + "\n")
    geometry = []
    for col, row in ((10, 4), (30, 19)):
        def positioned(s):
            g = grid(s)
            if (g["cursor_x"], g["cursor_y"]) != (col, row):
                return False
            x = g["rect"][0] + col*g["cell_width"]
            y = g["rect"][1] + row*g["cell_height"]
            # SessionOptions expects the same logical pixels as the rendered
            # grid. Ebitengine performs the native coordinate conversion.
            expected = [x, y, x+g["cell_width"], y+g["cell_height"]]
            return s["ime_caret_bounds"] == expected
        state = wait(positioned, "IME bounds follow cursor")
        geometry.append(state["ime_caret_bounds"])
        ctl("pane", "input", "--pane", pane, "--text", "x")
    (directory / "result.json").write_text(json.dumps({"cursor_combinations": results,
        "ime_bounds": geometry, "display_scale": initial["display_scale"]}, indent=2))
    print("PASS six cursor combinations, settings persistence, native IME caret geometry follows cursor", flush=True)
    print("artifacts=" + str(directory), flush=True)
finally:
    try:
        ctl("server", "shutdown")
    except Exception:
        pass
    if gui.poll() is None:
        gui.terminate()
    try:
        gui.wait(timeout=5)
    except subprocess.TimeoutExpired:
        gui.kill()
        gui.wait(timeout=5)
    log.close()
