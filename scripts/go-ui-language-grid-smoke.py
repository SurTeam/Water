#!/usr/bin/env python3
"""Verify locales, persisted startup grids and actual rendered controls through Water ctl."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
from PIL import Image


root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
directory = Path(tempfile.mkdtemp(prefix="water-language-grid.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
gui = None
server_pid = None
logs = []


def ctl(*args):
    result = subprocess.run([water, "ctl", "--socket", socket, *args], capture_output=True, timeout=6)
    if result.returncode:
        raise RuntimeError(result.stderr.decode(errors="replace"))
    if args == ("server", "shutdown"):
        return result.stdout.decode()
    return json.loads(result.stdout)


def wait(predicate, description):
    deadline = time.monotonic() + 8
    state = {}
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            details = (directory / f"gui-{len(logs)-1}.log").read_text()
            raise RuntimeError(f"Owned GUI exited: {description}; {details}; artifacts: {directory}")
        try:
            state = ctl("ui", "snapshot")
            if predicate(state):
                return state
        except RuntimeError:
            pass
        time.sleep(.025)
    (directory / "failure.json").write_text(json.dumps(state, indent=2))
    raise RuntimeError(f"Timeout: {description}; artifacts: {directory}")


def start():
    global gui, server_pid
    log = (directory / f"gui-{len(logs)}.log").open("w")
    logs.append(log)
    gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], stdout=log, stderr=subprocess.STDOUT)
    state = wait(lambda s: s.get("terminal_grids") and s.get("native_menu", {}).get("ready"), "initial grid")
    server_pid = ctl("server", "info")["server_pid"]
    print(f"owned_gui_pid={gui.pid} owned_server_pid={server_pid} socket={socket}", flush=True)
    return state


def click(label=None, kind=None):
    found = {}
    def ready(state):
        for hit in state["automation_hits"]:
            if (label is None or hit.get("label") == label) and (kind is None or hit["kind"] == kind):
                found.update(hit)
                return True
        if label and label.startswith("field:"):
            group = label.split(":")[1].split(".")[0]
            names = [f["name"] for f in state["settings_fields"] if f["name"].startswith(group + ".")]
            visible = [h["label"].removeprefix("field:") for h in state["automation_hits"] if h.get("label", "").startswith("field:" + group + ".")]
            direction = -1 if visible and names.index(label.removeprefix("field:")) < names.index(visible[0]) else 1
            width, height = state["frame_size"]
            ctl("ui", "wheel", "--x", str(width*.65), "--y", str(height*.5), "--dy", str(direction*88))
        return False
    wait(ready, label or kind)
    x0, y0, x1, y1 = found["rect"]
    return ctl("ui", "click", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2))


def edit(label, value):
    click(label=label)
    ctl("ui", "key", "cmd-a")
    ctl("ui", "key", "text:" + str(value))


def check_grid(columns, rows):
    state = wait(lambda s: len(s["terminal_grids"]) == 1 and s["terminal_grids"][0]["columns"] == columns and s["terminal_grids"][0]["rows"] == rows, f"{columns}x{rows} fitted grid")
    grid = state["terminal_grids"][0]
    x0, y0, x1, y1 = grid["rect"]
    assert x1-x0 == columns*grid["cell_width"] and y1-y0 == rows*grid["cell_height"]
    # Verify the real shell sees the same dimensions, not just layout metadata.
    pane, terminal = str(state["frame_focused_pane"]), str(state["frame_active_terminal"])
    ctl("pane", "input", "--pane", pane, "--text", "printf 'WATER_GRID_%s_%s\\n' $(stty size)\n")
    ctl("terminal", "contains", "--terminal", terminal, f"WATER_GRID_{rows}_{columns}", "--timeout-ms", "5000")
    print(f"PASS grid {columns}x{rows}; cell {grid['cell_width']}x{grid['cell_height']}; window {state['window_size']}", flush=True)
    return state


def check_tabs_and_settings():
    def tabs(state):
        return [hit for hit in state["automation_hits"] if hit["kind"] == "tab"]
    def aligned(state):
        resize = next((h for h in state["automation_hits"] if h["kind"] == "sidebar_resize"), None)
        return tabs(state) and resize and tabs(state)[0]["rect"][0] == resize["rect"][2]
    state = wait(aligned, "tab and terminal left alignment")
    pane = str(state["frame_focused_pane"])
    ctl("pane", "input", "--pane", pane, "--text", "sleep 30\n")
    wait(lambda s: tabs(s) and tabs(s)[0].get("label") == "sleep", "foreground sleep tab name")
    ctl("pane", "input", "--pane", pane, "--text", "\u0003")
    wait(lambda s: tabs(s) and tabs(s)[0].get("label") == "zsh", "shell tab name restored")
    ctl("ui", "key", "cmd-shift-t")
    wait(lambda s: s["rename_visible"], "custom tab editor")
    custom = "我的自定义标签名称很长，并且优先于当前命令"
    ctl("ui", "key", "cmd-a")
    ctl("ui", "key", "text:" + custom)
    ctl("ui", "key", "enter")
    wait(lambda s: not s["rename_visible"] and tabs(s)[0].get("label") == custom, "custom tab caption")
    before = directory / "before-settings.png"
    after = directory / "settings-sections.png"
    ctl("ui", "screenshot", "--output", str(before))
    ctl("ui", "key", "cmd-,")
    state = wait(lambda s: s["settings_visible"] and s.get("settings_sections"), "settings section headings")
    names = [f["name"] for f in state["settings_fields"]]
    assert "Terminal.DefaultColumns" not in names and "Terminal.DefaultLines" not in names
    assert names.count("Startup.WindowColumns") == names.count("Startup.WindowRows") == 1
    assert all(f["section"] for f in state["settings_fields"])
    ctl("ui", "screenshot", "--output", str(after))
    a, b = Image.open(before).convert("RGB"), Image.open(after).convert("RGB")
    width, height = a.size
    scale = state["frame_size"][0] / state["window_size"][0]
    card_height = min(round(540*scale), height-round(96*scale))
    bottom = (height+card_height)//2
    for point in [(10,height//2), (width-20,height//2), (width//2,bottom+round(3*scale))]:
        assert a.getpixel(point) == b.getpixel(point), ("settings dim/shadow",point,a.getpixel(point),b.getpixel(point))
    click(label="category:UI")
    edit("field:UI.TabFontSize", 20)
    edit("field:UI.TabMaxTitleLength", 16)
    click(label="save")
    wait(lambda s: s.get("settings_message") == "Settings saved" and s["tab_font_size"] == 20, "independent tab font")
    click(label="cancel")
    state = wait(lambda s: not s["settings_visible"], "close saved font settings")
    assert state["ui_config"]["font_size"] == 12
    expected = custom[:15] + "…"
    wait(lambda s: tabs(s)[0].get("label") == expected, "bounded custom caption")
    ctl("pane", "input", "--pane", pane, "--text", "sleep 30\n")
    def custom_over_command(state):
        dump = ctl("state")
        projections = [t for w in dump["workspaces"] for t in w["tabs"]]
        return any(t["tree"]["terminal"]["summary"]["process_name"] == "sleep" for t in projections) and tabs(state)[0].get("label") == expected
    wait(custom_over_command, "custom caption wins during foreground command")
    ctl("pane", "input", "--pane", pane, "--text", "\u0003")
    ctl("ui", "key", "cmd-e")
    def titlebar_tabs(s):
        return tabs(s) and all(0 <= h["rect"][1] < h["rect"][3] <= s["titlebar_height"] for h in tabs(s))
    wait(lambda s: not s["sidebar_visible"] and titlebar_tabs(s), "tabs remain in titlebar without sidebar")
    ctl("ui", "screenshot", "--output", str(directory / "tabs-no-sidebar.png"))
    ctl("ui", "key", "cmd-e")
    wait(lambda s: s["sidebar_visible"] and aligned(s), "tab alignment with sidebar restored")
    ctl("ui", "screenshot", "--output", str(directory / "tabs-font20-custom.png"))
    print("PASS dynamic/custom tab captions, title limit, independent font, left alignment, sections and undimmed settings background", flush=True)


def stop():
    global gui, server_pid
    if gui is not None and server_pid is None and Path(socket).exists():
        try:
            server_pid = ctl("server", "info")["server_pid"]
        except (RuntimeError, subprocess.SubprocessError):
            pass
    if server_pid is not None:
        try:
            if ctl("server", "info")["server_pid"] == server_pid:
                ctl("server", "shutdown")
        except (RuntimeError, subprocess.SubprocessError):
            pass
    if gui is not None and gui.poll() is None:
        gui.terminate()
        try:
            gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gui.kill()
            gui.wait(timeout=5)
    gui, server_pid = None, None


try:
    config.write_text(json.dumps({"startup": {"window_columns": 103, "window_rows": 37, "window_width": 1100},
        "terminal": {"font_family": "Sarasa Term SC Nerd Font", "font_size": 16, "line_height": 18},
        "ui": {"language": "en"}, "shell": {"program": shell, "args": ["-f"]},
        "server": {"detached": True, "detach_on_quit": True}}))
    start()
    check_grid(103, 37)
    check_tabs_and_settings()
    ctl("ui", "screenshot", "--output", str(directory / "english-window.png"))
    ctl("ui", "key", "cmd-,")
    click(label="category:UI")
    click(label="language:zh-Hans")
    preview = wait(lambda s: s["ui_language"] == "zh-Hans", "Chinese preview")
    assert preview["ui_strings"]["save"] == "保存"
    assert all(not f["name"].startswith("Startup.WindowWidth") for f in preview["settings_fields"])
    assert next(f for f in preview["settings_fields"] if f["name"] == "UI.Language")["label"] == "界面语言"
    ctl("ui", "screenshot", "--output", str(directory / "chinese-settings.png"))
    click(label="category:Startup")
    edit("field:Startup.WindowColumns", 104)
    edit("field:Startup.WindowRows", 38)
    click(label="save")
    wait(lambda s: s.get("settings_message") == "Settings saved" and s["ui_language"] == "zh-Hans" and s["native_menu"]["items"]["quit-gui"]["title"] == "退出界面", "saved Chinese menu")
    click(label="cancel")
    saved = json.loads(config.read_text())
    assert saved["ui"]["language"] == "zh-Hans" and saved["startup"]["window_columns"] == 104
    assert "window_width" not in saved["startup"]
    ctl("ui", "menu", "quit-gui")
    assert gui.wait(timeout=5) == 0
    start()
    check_grid(104, 38)
    ctl("ui", "screenshot", "--output", str(directory / "chinese-window.png"))
    ctl("ui", "key", "cmd-,")
    click(label="category:UI")
    click(label="language:en")
    ctl("ui", "screenshot", "--output", str(directory / "english-settings.png"))
    click(label="save")
    wait(lambda s: s.get("settings_message") == "Settings saved" and s["ui_language"] == "en" and s["native_menu"]["items"]["quit-gui"]["title"] == "Quit GUI", "English menu restored")
    click(label="cancel")
    ctl("ui", "key", "cmd-,")
    click(label="category:UI")
    click(label="language:zh-Hans")
    wait(lambda s: s["ui_language"] == "zh-Hans", "unsaved Chinese preview")
    ctl("ui", "key", "Space")
    wait(lambda s: s["ui_language"] == "en", "keyboard language selection")
    click(label="language:zh-Hans")
    click(label="cancel")
    wait(lambda s: s["settings_visible"] and "Unsaved changes" in s.get("settings_message", ""), "dirty language discard prompt")
    click(label="cancel")
    wait(lambda s: not s["settings_visible"] and s["ui_language"] == "en", "cancel restores language")
    assert json.loads(config.read_text())["ui"]["language"] == "en"
    stop()
    # Larger text and no sidebar must still produce exactly the requested grid.
    config.write_text(json.dumps({"startup": {"window_columns": 89, "window_rows": 29},
        "terminal": {"font_family": "Go Mono", "font_size": 22, "line_height": 23},
        "ui": {"language": "zh-Hans", "sidebar_visible": False},
        "shell": {"program": shell, "args": ["-f"]}, "server": {"detached": True}}))
    start()
    check_grid(89, 29)
    ctl("ui", "screenshot", "--output", str(directory / "large-font-no-sidebar.png"))
    stop()
    config.write_text(json.dumps({"startup": {"window_columns": 40, "window_rows": 24},
        "terminal": {"font_family": "Go Mono", "font_size": 8, "line_height": 8},
        "ui": {"sidebar_visible": True}, "shell": {"program": shell, "args": ["-f"]},
        "server": {"detached": True}}))
    start()
    check_grid(40, 24)
    print(f"PASS language selection, native menus, restart persistence, font/sidebar grid sizing and shell dimensions; artifacts={directory}", flush=True)
finally:
    stop()
    for log in logs:
        log.close()
