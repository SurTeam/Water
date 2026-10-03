#!/usr/bin/env python3
"""Exercise the native Go GUI through Water's control API in an owned instance."""
import json
import os
import platform
from pathlib import Path
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parent.parent
if platform.system() == "Linux" and not (os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY")):
    raise RuntimeError("A native display is required for GUI verification")
water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
directory = Path(tempfile.mkdtemp(prefix="water-settings.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next((p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists()), "/bin/sh")
config.write_text(json.dumps({
    "startup": {"window_width": 960, "window_height": 640},
    "server": {"detached": False, "detach_on_quit": False},
    "shell": {"program": shell, "args": ["-f"] if shell.endswith("zsh") else []},
    "future_setting": {"keep": True},
}))
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
    snapshot = {}
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError("GUI exited: " + (directory / "gui.log").read_text())
        try:
            snapshot = ctl("ui", "snapshot")
            if predicate(snapshot):
                return snapshot
        except RuntimeError:
            pass
        time.sleep(.025)
    (directory / "failure-snapshot.json").write_text(json.dumps(snapshot, indent=2))
    ctl("ui", "screenshot", "--output", str(directory / "failure.png"))
    raise RuntimeError("Timeout: " + description + " artifacts=" + str(directory))

def hit(label=None, kind=None):
    found = {}
    def ready(snapshot):
        for candidate in snapshot.get("automation_hits", []):
            if (label is None or candidate.get("label") == label) and (kind is None or candidate["kind"] == kind):
                found.update(candidate)
                return True
        if label and label.startswith("field:") and snapshot.get("settings_visible"):
            width, height = snapshot["frame_size"]
            ctl("ui", "wheel", "--x", str(width*.65), "--y", str(height*.5), "--dy", "120")
        return False
    wait(ready, f"hit {label or kind}")
    x0, y0, x1, y1 = found["rect"]
    assert ctl("ui", "click", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2))["handled"]

def edit(label, value):
    hit(label="field:" + label)
    select_all = "cmd-a" if platform.system() == "Darwin" else "ctrl-a"
    assert ctl("ui", "key", select_all)["handled"], "field focus: " + label
    assert ctl("ui", "key", "text:" + value)["handled"], "field edit: " + label

try:
    wait(lambda s: s.get("frame_active_terminal") not in (None, "00000000-0000-0000-0000-000000000000"), "initial terminal")
    ctl("ui", "key", "cmd-,")
    wait(lambda s: s.get("settings_visible"), "settings open")
    ctl("ui", "screenshot", "--output", str(directory / "settings.png"))
    edit("Terminal.FontSize", "18")
    edit("Terminal.LineHeight", "22")
    hit(label="category:UI")
    edit("UI.SidebarWidth", "240")
    hit(label="category:Theme")
    edit("Theme.TerminalBackground", "#182838")
    edit("Theme.1", "#ee5566")
    hit(label="category:Shortcuts")
    edit("Shortcuts.SplitDown", "ctrl-alt-s")
    hit(label="save")
    saved = wait(lambda s: not s.get("settings_visible") and s.get("effective_config", {}).get("ui", {}).get("sidebar_width") == 240, "saved settings projected")
    persisted = json.loads(config.read_text())
    assert persisted["future_setting"]["keep"]
    assert persisted["theme"]["terminal_background"] == "#182838"
    assert persisted["shortcuts"]["split_down"] == "ctrl-alt-s"
    assert persisted["terminal"]["font_size"] == 18
    assert persisted["theme"]["ansi_colors"][1] == "#ee5566"
    ctl("ui", "key", "cmd-\\")
    split = wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 2, "horizontal split")
    divider = next(h for h in split["automation_hits"] if h["kind"] == "divider")
    before = [h["rect"][2]-h["rect"][0] for h in split["automation_hits"] if h["kind"] == "pane"]
    x0, y0, x1, y1 = divider["rect"]
    x, y = (x0+x1)/2, (y0+y1)/2
    assert ctl("ui", "drag", "--x", str(x), "--y", str(y), "--to-x", str(x+120), "--to-y", str(y))["handled"]
    wait(lambda s: any(abs((h["rect"][2]-h["rect"][0])-before[0]) > 60 for h in s.get("automation_hits", []) if h["kind"] == "pane"), "divider drag committed")
    ctl("ui", "key", "ctrl-alt-s")
    wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 3, "configured vertical split")
    ctl("ui", "key", "cmd-shift-enter")
    wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 1, "promote pane")
    ctl("ui", "key", "cmd-[")
    wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 2, "previous tab")
    ctl("ui", "key", "cmd-2")
    wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 1, "numbered tab")
    ctl("ui", "key", "cmd-1")
    first = wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 2, "return to first tab")
    workspace = first["frame_active_workspace"]
    ctl("ui", "key", "cmd-shift-n")
    wait(lambda s: s.get("frame_active_workspace") != workspace, "new workspace")
    ctl("ui", "key", "ctrl-shift-tab")
    wait(lambda s: s.get("frame_active_workspace") == workspace, "previous workspace")
    ctl("ui", "screenshot", "--output", str(directory / "split.png"))
    ctl("ui", "key", "cmd-,")
    wait(lambda s: s.get("settings_visible"), "reopen settings")
    ctl("ui", "key", "Escape")
    wait(lambda s: not s.get("settings_visible"), "cancel settings")
    print("PASS settings persistence, runtime appearance, configured splitting, divider drag, pane promotion, numbered tabs, workspace cycling, cancel", flush=True)
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
