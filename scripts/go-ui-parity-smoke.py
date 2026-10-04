#!/usr/bin/env python3
"""Verify migrated Rust settings and native rounded geometry in an owned GUI."""
import json
import os
import platform
import subprocess
import tempfile
import time
from pathlib import Path
from PIL import Image

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
directory = Path(tempfile.mkdtemp(prefix="water-parity.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"server": {"detached": False}, "shell": {"program": shell, "args": ["-f"]},
                             "startup": {"window_columns": 96, "window_rows": 30}}))
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
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError((directory / "gui.log").read_text())
        try:
            state = ctl("ui", "snapshot")
            if predicate(state):
                return state
        except RuntimeError:
            pass
        time.sleep(.025)
    raise RuntimeError(f"Timeout: {description}; artifacts={directory}")

def click_hit(hit):
    x0, y0, x1, y1 = hit["rect"]
    return ctl("ui", "click", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2))

def hit(label=None, kind=None):
    found = {}
    def ready(state):
        for h in state["automation_hits"]:
            if (label is None or h.get("label") == label) and (kind is None or h["kind"] == kind):
                found.update(h)
                return True
        if label and label.startswith("field:"):
            # The catalog records field order, so seeking also works upwards.
            group = label.split(":")[1].split(".")[0]
            names = [f["name"] for f in state["settings_fields"] if f["name"].startswith(group+".")]
            target = names.index(label.removeprefix("field:"))
            visible = [h["label"].removeprefix("field:") for h in state["automation_hits"]
                       if h.get("label", "").startswith("field:"+group+".")]
            direction = -1 if visible and target < names.index(visible[0]) else 1
            width, height = state["frame_size"]
            ctl("ui", "wheel", "--x", str(width*.65), "--y", str(height*.5), "--dy", str(direction*88))
        return False
    wait(ready, label or kind)
    assert click_hit(found)["handled"]
    return found

def edit(name, value):
    hit(label="field:"+name)
    assert ctl("ui", "key", "cmd-a")["handled"]
    assert ctl("ui", "key", "text:"+str(value))["handled"]

def save():
    hit(label="save")
    wait(lambda s: s["settings_visible"] and s.get("settings_message") == "Settings saved", "saved settings remain open")
    hit(label="cancel")
    return wait(lambda s: not s["settings_visible"], "saved settings")

def screenshot(name, rounded):
    path = directory / (name+".png")
    ctl("ui", "screenshot", "--output", str(path))
    im = Image.open(path).convert("RGBA")
    for x, y in ((0, 0), (im.width-1, 0), (0, im.height-1), (im.width-1, im.height-1)):
        assert (im.getpixel((x, y))[3] == 0) == rounded, (name, x, y, im.getpixel((x, y)))
    assert im.getpixel((im.width//2, 0))[3] == 255

try:
    initial = wait(lambda s: s.get("frame_active_terminal") not in (None, "00000000-0000-0000-0000-000000000000"), "terminal")
    scale = initial["titlebar_height"] / 36
    if platform.system() == "Darwin":
        wait(lambda s: s["native_menu"].get("window_corner_radius") == 16 and
             s["native_menu"].get("window_masks_to_bounds") and not s["native_menu"]["window_opaque"], "native rounded layer")
    screenshot("rounded", True)
    ctl("ui", "key", "cmd-shift-e")
    wait(lambda s: s["rename_visible"], "workspace rename")
    ctl("ui", "key", "text:Elegant workspace")
    ctl("ui", "key", "enter")
    wait(lambda s: not s["rename_visible"] and any(w["title"] == "Elegant workspace" for w in ctl("state")["workspaces"]), "renamed workspace")
    ctl("ui", "key", "cmd-shift-t")
    wait(lambda s: s["rename_visible"], "tab rename")
    ctl("ui", "key", "text:Development")
    ctl("ui", "key", "enter")
    wait(lambda s: not s["rename_visible"] and any(t["title"] == "Development" for w in ctl("state")["workspaces"] for t in w["tabs"]), "renamed tab")
    ctl("ui", "key", "cmd-shift-k")
    wait(lambda s: s["remote_form_visible"], "remote shortcut")
    ctl("ui", "key", "escape")
    wait(lambda s: not s["remote_form_visible"], "remote cancelled")
    ctl("ui", "key", "cmd-,")
    state = wait(lambda s: s["settings_visible"], "settings")
    fields = {f["name"] for f in state["settings_fields"]}
    assert {"Startup.ControlSocket", "UI.WindowCornerRadius", "UI.SidebarAgentRowWidth",
            "Theme.AgentColors.codex", "Theme.CursorForeground", "Shortcuts.NewWindow", "Shortcuts.RenameTab"} <= fields
    hit(label="category:UI")
    for name, value in (("TitlebarHeight", 40), ("TabHeight", 28), ("SidebarAgentRowWidth", .9),
                        ("WindowCornerRadius", 24), ("SidebarWidth", 260), ("PanePadding", 14)):
        edit("UI."+name, value)
    hit(label="category:Theme")
    edit("Theme.AgentColors.codex", "#abcdef")
    state = save()
    assert state["ui_config"]["titlebar_height"] == 40
    assert state["sidebar_width"] == round(260*scale), state["sidebar_width"]
    persisted = json.loads(config.read_text())
    assert persisted["theme"]["agent_colors"]["codex"] == "#abcdef"
    assert persisted["ui"]["window_corner_radius"] == 24
    wait(lambda s: s["native_menu"].get("window_corner_radius") == 24, "live native radius")
    screenshot("custom-rounded", True)
    resize = hit(kind="sidebar_resize")
    x0, y0, x1, y1 = resize["rect"]
    ctl("ui", "drag", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2),
        "--to-x", str((x0+x1)/2+40), "--to-y", str((y0+y1)/2))
    wait(lambda s: s["sidebar_width"] > state["sidebar_width"], "sidebar resize")
    ctl("ui", "key", "cmd-,")
    hit(label="category:UI")
    edit("UI.WindowCornerRadius", 0)
    save()
    wait(lambda s: s["native_menu"].get("window_corner_radius") == 0, "square native layer")
    screenshot("square", False)
    ctl("ui", "key", "cmd-,")
    hit(label="category:UI")
    edit("UI.WindowCornerRadius", 16)
    hit(label="field:UI.SidebarVisible")
    save()
    wait(lambda s: not any(h["kind"] == "sidebar_resize" for h in s["automation_hits"]), "sidebar visibility")
    ctl("ui", "key", "cmd-e")
    wait(lambda s: any(h["kind"] == "sidebar_resize" for h in s["automation_hits"]), "sidebar toggle")
    hit(kind="window_maximize")
    wait(lambda s: s["window_maximized"] and s["native_menu"].get("window_corner_radius") == 0, "maximized square corners")
    screenshot("maximized", False)
    hit(kind="window_maximize")
    wait(lambda s: not s["window_maximized"] and s["native_menu"].get("window_corner_radius") == 16, "restored corners")
    screenshot("restored", True)
    # Spawn the scenario's real agent process through the command dispatcher;
    # clicking its row must activate the owning tab and pane.
    ctl("terminal", "spawn", "--program", shell, "--", "-f", "-c", "exec -a codex /bin/cat")
    agent_state = wait(lambda s: any(h["kind"] == "agent" for h in s["automation_hits"]), "foreground agent row")
    agent_hit = next(h for h in agent_state["automation_hits"] if h["kind"] == "agent")
    agent_pane = agent_hit["id"]
    ctl("ui", "key", "cmd-t")
    wait(lambda s: s["frame_focused_pane"] != agent_pane, "new tab")
    hit(kind="agent")
    wait(lambda s: s["frame_focused_pane"] == agent_pane, "agent sidebar focuses owning tab/pane")
    screenshot("agent", True)
    ctl("ui", "key", "ctrl-c")
    print(f"PASS native and rendered corners, square/maximized/restore; migrated settings controls, live geometry, agent colors, visibility, resizing and rename/remote shortcuts\nartifacts={directory}", flush=True)
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
