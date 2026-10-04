#!/usr/bin/env python3
"""Exercise the native Go GUI through Water's control API in an owned instance."""
import json
import os
import platform
import shlex
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
    "startup": {"window_columns": 96, "window_rows": 30},
    "terminal": {"font_family": "Sarasa Term SC Nerd Font"} if platform.system() == "Darwin" and (Path.home()/"Library/Fonts/sarasa-term-sc-regular-nerd-font.ttf").exists() else {},
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
            group = label.split(":")[1].split(".")[0]
            names = [f["name"] for f in snapshot["settings_fields"] if f["name"].startswith(group+".")]
            target = names.index(label.removeprefix("field:"))
            visible = [h["label"].removeprefix("field:") for h in snapshot["automation_hits"] if h.get("label", "").startswith("field:"+group+".")]
            direction = -1 if visible and target < names.index(visible[0]) else 1
            width, height = snapshot["frame_size"]
            ctl("ui", "wheel", "--x", str(width*.65), "--y", str(height*.5), "--dy", str(direction*88))
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
    initial = wait(lambda s: s.get("frame_active_terminal") not in (None, "00000000-0000-0000-0000-000000000000"), "initial terminal")
    assert initial["renderer"] == "ebitengine"
    assert initial["custom_titlebar"] and not initial["window_decorated"], {k: initial[k] for k in ("renderer", "window_decorated", "custom_titlebar")}
    if json.loads(config.read_text()).get("terminal", {}).get("font_family") == "Sarasa Term SC Nerd Font":
        font = initial["terminal_font"]
        assert font["status"] == "ready" and font["resolved_family"].replace(" ", "") == "SarasaTermSCNerdFont", font
        assert font["path"].endswith("sarasa-term-sc-regular-nerd-font.ttf"), font
        for name, suffix in (("bold", "bold"), ("italic", "italic"), ("bold_italic", "bolditalic")):
            resolved = initial["terminal_font_styles"][name]
            assert resolved["status"] == "ready" and resolved["path"].endswith("sarasa-term-sc-"+suffix+"-nerd-font.ttf"), resolved
        print("font_verified=" + font["path"], flush=True)
    terminal = initial["frame_active_terminal"]
    pane = initial["frame_focused_pane"]
    # Preserve actual current directories, with and without OSC 7, before model
    # creation changes the active pane. Use paths requiring URI/shell escaping.
    from urllib.parse import quote
    inherited = directory / "项目 with spaces"
    inherited.mkdir()
    ctl("pane", "input", "--pane", pane, "--text", "cd "+shlex.quote(str(inherited))+"; printf '\\033]7;%s\\007' "+shlex.quote("file://localhost"+quote(str(inherited)))+"\n")
    wait(lambda s: any(g["pane_id"]==pane and g["working_directory_uri"].endswith(quote(str(inherited))) for g in s["terminal_grids"]), "OSC 7 current directory")
    ctl("ui", "key", "cmd-t")
    child = wait(lambda s: s["frame_focused_pane"] != pane, "new tab inherits source context")
    ctl("pane", "input", "--pane", child["frame_focused_pane"], "--text", "printf 'WATER_TAB_CWD_%s\\n' \"$PWD\"\n")
    ctl("terminal", "contains", "--terminal", child["frame_active_terminal"], "WATER_TAB_CWD_"+str(inherited.resolve()), "--timeout-ms", "4000")
    child_tab = next(w["active_tab"] for w in ctl("state")["workspaces"] if w["id"] == child["frame_active_workspace"])
    ctl("tab", "close", "--tab", child_tab)
    wait(lambda s: s["frame_focused_pane"] == pane, "source pane restored")
    process_dir = directory / "process cwd"
    process_dir.mkdir()
    ctl("pane", "input", "--pane", pane, "--text", "cd "+shlex.quote(str(process_dir))+"; printf 'WATER_CWD_CHANGED\\n'\n")
    ctl("terminal", "contains", "--terminal", terminal, "WATER_CWD_CHANGED", "--timeout-ms", "5000")
    ctl("ui", "key", "cmd-t")
    child = wait(lambda s: s["frame_focused_pane"] != pane, "tab uses current process cwd despite stale OSC 7")
    ctl("pane", "input", "--pane", child["frame_focused_pane"], "--text", "printf 'WATER_FRESH_CWD_%s\\n' \"$PWD\"\n")
    ctl("terminal", "contains", "--terminal", child["frame_active_terminal"], "WATER_FRESH_CWD_"+str(process_dir.resolve()), "--timeout-ms", "4000")
    child_tab = next(w["active_tab"] for w in ctl("state")["workspaces"] if w["id"] == child["frame_active_workspace"])
    ctl("tab", "close", "--tab", child_tab)
    wait(lambda s: s["frame_focused_pane"] == pane, "source pane restored after fresh cwd tab")
    ctl("pane", "split", "--pane", pane, "--right")
    child = wait(lambda s: s["frame_focused_pane"] != pane, "split inherits live process cwd")
    ctl("pane", "input", "--pane", child["frame_focused_pane"], "--text", "printf 'WATER_SPLIT_CWD_%s\\n' \"$PWD\"\n")
    ctl("terminal", "contains", "--terminal", child["frame_active_terminal"], "WATER_SPLIT_CWD_"+str(process_dir.resolve()), "--timeout-ms", "4000")
    ctl("pane", "close", "--pane", child["frame_focused_pane"])
    wait(lambda s: s["frame_focused_pane"] == pane, "source pane restored after split")
    # Capture bytes in a real raw PTY so shell editing cannot mask bad encoding.
    python = str(Path.home() / ".venv/bin/python")
    key_probes = [("Up", b"\x1b[A"), ("Down", b"\x1b[B"), ("Left", b"\x1b[D"), ("Right", b"\x1b[C"),
                  ("Backspace", b"\x7f"), ("Delete", b"\x1b[3~"), ("Insert", b"\x1b[2~"),
                  ("Home", b"\x1b[H"), ("End", b"\x1b[F"), ("PageUp", b"\x1b[5~"), ("PageDown", b"\x1b[6~"),
                  ("Tab", b"\t"), ("shift-Tab", b"\x1b[Z"), ("Return", b"\r"), ("Escape", b"\x1b"),
                  ("ctrl-a", b"\x01"), ("ctrl-c", b"\x03"), ("ctrl-l", b"\x0c"), ("ctrl-z", b"\x1a"), ("ctrl-2", b"\x00"),
                  ("ctrl-[", b"\x1b"), ("ctrl-\\", b"\x1c"), ("ctrl-]", b"\x1d"), ("ctrl-6", b"\x1e"), ("ctrl-minus", b"\x1f"),
                  ("alt-x", b"\x1bx"), ("alt-shift-x", b"\x1bX"), ("alt-'", b"\x1b'"), ("alt-;", b"\x1b;"),
                  ("ctrl-alt-c", b"\x1b\x03"), ("ctrl-shift-Up", b"\x1b[1;6A"), ("alt-Backspace", b"\x1b\x7f"),
                  ("F1", b"\x1bOP"), ("F12", b"\x1b[24~"), ("F13", b"\x1b[25~"), ("F20", b"\x1b[34~"), ("F24", b"\x1b[45~")]
    expected_bytes = b"".join(data for _, data in key_probes)
    raw_probe = """import os, termios, tty
old = termios.tcgetattr(0)
try:
    tty.setraw(0)
    print('WATER_' + 'RAW_READY', flush=True)
    data = b''
    while len(data) < COUNT:
        data += os.read(0, COUNT-len(data))
finally:
    termios.tcsetattr(0, termios.TCSANOW, old)
print('WATER_KEYS_' + data.hex(), flush=True)
""".replace("COUNT", str(len(expected_bytes)))
    ctl("pane", "input", "--pane", pane, "--text", python+" -c "+shlex.quote(raw_probe)+"\n")
    ctl("terminal", "contains", "--terminal", terminal, "WATER_RAW_READY", "--timeout-ms", "5000")
    # No selected text: Command+C must copy only and contribute no PTY bytes.
    assert ctl("ui", "key", "cmd-c")["handled"]
    ctl("ui", "key", "cmd-d")
    for key, _ in key_probes:
        assert ctl("ui", "key", key)["handled"], key
    ctl("terminal", "contains", "--terminal", terminal, "WATER_KEYS_"+expected_bytes.hex(), "--timeout-ms", "5000")
    mouse_probe = """import os, select, termios, time, tty
old = termios.tcgetattr(0)
try:
    tty.setraw(0)
    print('\\x1b[?1000h\\x1b[?1006hWATER_' + 'MOUSE_READY', flush=True)
    data = b''
    deadline = time.monotonic()+4
    while time.monotonic() < deadline:
        readable, _, _ = select.select([0], [], [], .25 if data else max(0,deadline-time.monotonic()))
        if not readable:
            break
        data += os.read(0, 4096)
finally:
    print('\\x1b[?1000l\\x1b[?1006l', end='', flush=True)
    termios.tcsetattr(0, termios.TCSANOW, old)
print('WATER_MOUSE_COUNT_' + str(data.count(b'M')) + '_' + data.hex(), flush=True)
"""
    for delta, reports in ((40,1),(120,3),(10000,64)):
        probe = mouse_probe.replace("MOUSE_READY", "MOUSE_READY_"+str(delta)).replace("MOUSE_COUNT_", "MOUSE_COUNT_"+str(delta)+"_")
        ctl("pane", "input", "--pane", pane, "--text", python+" -c "+shlex.quote(probe)+"\n")
        ctl("terminal", "contains", "--terminal", terminal, "WATER_MOUSE_READY_"+str(delta), "--timeout-ms", "4000")
        mouse_state = ctl("ui", "snapshot")
        rect = next(h["rect"] for h in mouse_state["automation_hits"] if h["kind"] == "pane")
        ctl("ui", "wheel", "--x", str((rect[0]+rect[2])/2), "--y", str((rect[1]+rect[3])/2), "--dy", str(delta))
        ctl("terminal", "contains", "--terminal", terminal, f"WATER_MOUSE_COUNT_{delta}_{reports}_", "--timeout-ms", "4000")
    interrupt_probe = """import time
print('WATER_' + 'INTERRUPT_READY', flush=True)
try:
    time.sleep(30)
except KeyboardInterrupt:
    print('WATER_' + 'INTERRUPTED', flush=True)
"""
    ctl("pane", "input", "--pane", pane, "--text", python+" -c "+shlex.quote(interrupt_probe)+"\n")
    ctl("terminal", "contains", "--terminal", terminal, "WATER_INTERRUPT_READY", "--timeout-ms", "5000")
    assert ctl("ui", "key", "ctrl-c")["handled"]
    ctl("terminal", "contains", "--terminal", terminal, "WATER_INTERRUPTED", "--timeout-ms", "5000")
    ctl("pane", "input", "--pane", pane, "--text", "for i in {1..400}; do printf 'ROW_%04d\\n' $i; done; printf 'WATER_SCROLL_END\\n'\n")
    ctl("terminal", "contains", "--terminal", terminal, "WATER_SCROLL_END", "--timeout-ms", "4000")
    def grid(s):
        return next(g for g in s["terminal_grids"] if g["pane_id"] == pane)
    scrolling = wait(lambda s: grid(s)["y_base"] > 300 and grid(s)["y_disp"] == grid(s)["y_base"], "scrollback filled and at bottom")
    rect = grid(scrolling)["rect"]
    def wheel(delta):
        ctl("ui", "wheel", "--x", str((rect[0]+rect[2])/2), "--y", str((rect[1]+rect[3])/2), "--dy", str(delta))
    before = grid(scrolling)["y_disp"]
    wheel(-40)
    fine = wait(lambda s: grid(s)["y_disp"] == before-1, "fine scrollback moves one line")
    wheel(-4000)
    wait(lambda s: grid(s)["y_disp"] == before-101, "bulk scrollback moves 100 lines")
    wheel(-1e9)
    wait(lambda s: grid(s)["y_disp"] == 0, "very large scroll safely clamps to history start")
    wheel(1e9)
    wait(lambda s: grid(s)["y_disp"] == grid(s)["y_base"], "large scroll safely returns to live bottom")
    font_demo = "printf '\\033[2J\\033[HRegular  gypqj  0123456789  Il1 O0  => !=\\n\\033[1mBold     gypqj  0123456789  Il1 O0\\033[0m\\n\\033[3mItalic   gypqj  0123456789  Il1 O0\\033[0m\\n中文：更纱黑体  水\\nNerd:        \\n'\n"
    ctl("pane", "input", "--pane", pane, "--text", font_demo)
    ctl("terminal", "contains", "--terminal", terminal, "Nerd:", "--timeout-ms", "5000")
    ctl("ui", "screenshot", "--output", str(directory / "font.png"))
    unicode_demo = "printf '\\033[2J\\033[H中文标点：你好，世界。你好、世界；你好：世界！\\n中文括号：（你好）「世界」『文字』【布局】\\n全角文本：ＡＢＣ１２３￥＋－＝\\n组合字符：é ä 漢󠄀\\n绘图符号：┌──┬──┐ ▀▄█ ░▒▓ ⡇⣀⣿\\n文字符号：← → ↑ ↓ ∑ ∫ √ ≤ ≥ ▶︎ ♥︎\\nemoji: 🍺 👩‍💻  Nerd:  \\n'\n"
    unicode_demo = unicode_demo.replace("emoji:", "补充文字符号：🂡 🀀 🠖 🞀 🄰 🩀\\nemoji:")
    ctl("pane", "input", "--pane", pane, "--text", unicode_demo)
    ctl("terminal", "contains", "--terminal", terminal, "中文标点：", "--timeout-ms", "4000")
    ctl("ui", "screenshot", "--output", str(directory / "unicode-layout.png"))
    ctl("ui", "key", "ctrl-l")
    tabs = [h for h in initial["automation_hits"] if h["kind"] == "tab"]
    assert tabs and all(h["rect"][3] <= initial["titlebar_height"] for h in tabs), "tabs must share the titlebar"
    assert initial["titlebar_height"] == 36 * initial["frame_size"][0] / initial["window_size"][0]
    titlebar = next(h for h in initial["automation_hits"] if h["kind"] == "titlebar")
    x0, y0, x1, y1 = titlebar["rect"]
    # Use the dedicated blank gap between window controls and integrated tabs.
    scale = initial["frame_size"][0] / initial["window_size"][0]
    x, y = x0+2*scale, initial["titlebar_height"]/2
    original_position = initial["window_position"]
    assert ctl("ui", "drag", "--x", str(x), "--y", str(y), "--to-x", str(x+60), "--to-y", str(y+30))["handled"]
    wait(lambda s: s["window_position"] != original_position, "custom titlebar moved window")
    assert ctl("ui", "click", "--x", str(x), "--y", str(y), "--click-count", "2")["handled"]
    wait(lambda s: s["window_maximized"], "custom maximize")
    hit(kind="window_maximize")
    restored = wait(lambda s: not s["window_maximized"] and s["window_size"] == initial["window_size"], "custom restore")
    assert not restored["window_decorated"], "restoring must retain our custom titlebar"
    width, height = restored["frame_size"]
    assert ctl("ui", "drag", "--x", str(width-2), "--y", str(height*.8), "--to-x", str(width+58), "--to-y", str(height*.8))["handled"]
    enlarged = wait(lambda s: s["window_size"][0] > restored["window_size"][0], "custom border resize")
    width, height = enlarged["frame_size"]
    assert ctl("ui", "drag", "--x", str(width-2), "--y", str(height*.8), "--to-x", str(width-62), "--to-y", str(height*.8))["handled"]
    wait(lambda s: s["window_size"] == initial["window_size"], "custom border restored size")
    ctl("ui", "screenshot", "--output", str(directory / "workspace.png"))
    ctl("ui", "key", "cmd-,")
    wait(lambda s: s.get("settings_visible"), "settings open")
    ctl("ui", "screenshot", "--output", str(directory / "settings.png"))
    edit("Terminal.FontSize", "18")
    edit("Terminal.LineHeight", "22")
    font_chain = "Sarasa Term SC Nerd Font, Apple Color Emoji" if platform.system() == "Darwin" else "Go Mono, Noto Color Emoji"
    edit("Terminal.FontFamily", font_chain)
    hit(label="category:UI")
    edit("UI.SidebarWidth", "240")
    edit("UI.TitlebarHeight", "20")
    edit("UI.TabHeight", "12")
    edit("UI.TabFontSize", "8")
    edit("UI.SidebarRemoteButtonHeight", "40")
    edit("UI.SidebarRemoteButtonFontSize", "16")
    edit("UI.SidebarWorkspaceButtonHeight", "24")
    edit("UI.SidebarWorkspaceButtonFontSize", "9")
    hit(label="category:Theme")
    edit("Theme.TerminalBackground", "#182838")
    edit("Theme.1", "#ee5566")
    hit(label="category:Shortcuts")
    edit("Shortcuts.HideWindow", "cmd-alt-w")
    edit("Shortcuts.MinimizeWindow", "cmd-alt-m")
    edit("Shortcuts.IgnoreQuit", "cmd-alt-q")
    edit("Shortcuts.SplitDown", "ctrl-alt-s")
    hit(label="save")
    saved = wait(lambda s: s.get("settings_visible") and s.get("settings_message") == "Settings saved" and s.get("effective_config", {}).get("ui", {}).get("sidebar_width") == 240, "saved settings projected and panel remains open")
    hit(label="cancel")
    saved = wait(lambda s: not s.get("settings_visible"), "cancel saved settings without discard prompt")
    scale = saved["frame_size"][0] / saved["window_size"][0]
    assert saved["titlebar_height"] == round(20 * scale)
    tab = next(h for h in saved["automation_hits"] if h["kind"] == "tab")
    assert tab["rect"][3] - tab["rect"][1] == round(12 * scale)
    assert abs((tab["rect"][1] + tab["rect"][3]) / 2 - saved["titlebar_height"] / 2) <= 1
    resize = next(h for h in saved["automation_hits"] if h["kind"] == "sidebar_resize")
    assert tab["rect"][0] == resize["rect"][2], "tab must align with terminal container"
    assert not any(h["kind"] in ("settings", "split_right", "split_down") for h in saved["automation_hits"])
    remote_button = next(h for h in saved["automation_hits"] if h["kind"] == "new_remote")
    workspace_button = next(h for h in saved["automation_hits"] if h["kind"] == "new_workspace")
    assert remote_button["rect"][3]-remote_button["rect"][1] == round(40*scale)
    assert workspace_button["rect"][3]-workspace_button["rect"][1] == round(24*scale)
    assert workspace_button["rect"][1]-remote_button["rect"][3] == round(8*scale)
    assert saved["effective_config"]["ui"]["sidebar_remote_button_font_size"] == 16
    assert saved["effective_config"]["ui"]["sidebar_workspace_button_font_size"] == 9
    ctl("ui", "screenshot", "--output", str(directory / "compact-titlebar.png"))
    persisted = json.loads(config.read_text())
    assert persisted["future_setting"]["keep"]
    assert persisted["theme"]["terminal_background"] == "#182838"
    assert persisted["shortcuts"]["split_down"] == "ctrl-alt-s"
    assert persisted["shortcuts"]["hide_window"] == "cmd-alt-w"
    if platform.system() == "Darwin":
        wait(lambda s: s["native_menu"].get("items", {}).get("hide-window", {}).get("modifiers") == (1 << 20 | 1 << 19), "native menu reflects updated shortcut")
    assert persisted["terminal"]["font_size"] == 18
    assert persisted["terminal"]["font_family"] == font_chain
    assert persisted["theme"]["ansi_colors"][1] == "#ee5566"
    ctl("ui", "key", "cmd-\\")
    split = wait(lambda s: len([h for h in s.get("automation_hits", []) if h["kind"] == "pane"]) == 2, "horizontal split")
    scale = split["frame_size"][0] / split["window_size"][0]
    ui = split["effective_config"]["ui"]
    panes = [h for h in split["automation_hits"] if h["kind"] == "pane"]
    expected_top = split["titlebar_height"] + round((ui["window_padding"] + ui["pane_padding"]) * scale)
    cells = {str(g["pane_id"]): g for g in split["terminal_grids"]}
    assert all(0 <= h["rect"][1] - expected_top <= cells[str(h["id"])]["cell_height"]/2 + 1 for h in panes), "split panes must only center the sub-cell remainder, not reserve a process header"
    left_pane = min(panes, key=lambda h: h["rect"][0])
    resize = next(h for h in split["automation_hits"] if h["kind"] == "sidebar_resize")
    assert 0 <= left_pane["rect"][0] - round(ui["pane_padding"] * scale) - resize["rect"][2] <= cells[str(left_pane["id"])]["cell_width"]/2 + 1, "sidebar gap must only reserve the resize handle and centered sub-cell remainder"
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
    hit(label="category:UI")
    edit("UI.TitlebarHeight", "12")
    edit("UI.TabHeight", "8")
    edit("UI.TabFontSize", "6")
    hit(label="save")
    wait(lambda s: s.get("settings_message") == "Settings saved" and s["effective_config"]["ui"]["titlebar_height"] == 12, "smallest chrome saved")
    hit(label="save")
    wait(lambda s: s.get("settings_message") == "Settings saved" and not s.get("settings_saving"), "repeat save without leaving settings")
    hit(label="category:UI")
    ctl("ui", "screenshot", "--output", str(directory / "accent-settings.png"))
    ctl("ui", "key", "cmd-,")
    tiny = wait(lambda s: not s.get("settings_visible"), "settings shortcut closes saved panel")
    assert tiny["titlebar_height"] == round(12 * scale)
    tab = next(h for h in tiny["automation_hits"] if h["kind"] == "tab")
    assert tab["rect"][3] - tab["rect"][1] == round(8 * scale)
    assert abs((tab["rect"][1]+tab["rect"][3])/2-tiny["titlebar_height"]/2) <= 1
    assert all(0 <= h["rect"][1] < h["rect"][3] <= tiny["titlebar_height"] for h in tiny["automation_hits"] if h["kind"].startswith("window_"))
    ctl("ui", "screenshot", "--output", str(directory / "minimum-titlebar.png"))
    ctl("ui", "key", "cmd-,")
    ctl("ui", "key", "Escape")
    wait(lambda s: not s.get("settings_visible"), "Escape closes clean settings")
    hit(kind="window_minimize")
    wait(lambda s: s["window_minimized"], "custom minimize")
    hit(kind="window_maximize")
    restored = wait(lambda s: not s["window_minimized"], "restore minimized window")
    assert not restored["window_decorated"]
    hit(kind="window_close")
    assert gui.wait(timeout=5) == 0, "custom close must exit cleanly"
    print("PASS complete terminal key matrix and process interrupt; font family/styles, mini integrated titlebar tabs, move, maximize/restore, border resize, minimize, close; settings persistence, runtime appearance, configured splitting, divider drag, pane promotion, numbered tabs, workspace cycling, cancel", flush=True)
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
