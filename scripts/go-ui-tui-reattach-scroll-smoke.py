#!/usr/bin/env python3
"""Replay a fullscreen mouse TUI after trimming, then verify real wheel input."""
import os
import shlex
import subprocess
import sys

from water_test import WaterGUI

os.environ["WATER_TEST_UNFOCUSED"] = "1"

with WaterGUI("water-tui-reattach-scroll.", {"server": {"detached": True, "detach_on_quit": True}}) as test:
    pane = test.ctl("ui", "snapshot")["frame_focused_pane"]
    # Match the mouse initialization captured from the installed first-rule.
    # This local raw-mode fixture never submits a model request.
    app = test.save("mouse_tui.py", r'''
import os, re, tty
from pathlib import Path
tty.setraw(0)
os.write(1, b"\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h")
frame = b"\x1b[H" + b"WHEEL_REPLAY_FRAME" + b" " * 60
os.write(1, frame * 12000)
os.write(1, b"\x1b[2J\x1b[HWHEEL_REPLAY_READY")
pending = b""
while True:
    data = os.read(0, 4096)
    if not data:
        break
    with Path("wheel-input.bin").open("ab") as log:
        log.write(data)
    pending += data
    for match in re.finditer(rb"\x1b\[<(64|65);\d+;\d+M", pending):
        marker = b"UP" if match[1] == b"64" else b"DOWN"
        os.write(1, b"\x1b[2J\x1b[HWHEEL_RECEIVED_" + marker)
    if b"M" in pending:
        pending = pending[pending.rfind(b"M") + 1:]
''')
    command = shlex.quote(sys.executable) + " -u " + shlex.quote(str(app)) + "\n"
    test.ctl("pane", "input", "--pane", pane, "--text", command)
    test.wait(lambda: "WHEEL_REPLAY_READY" in test.content(pane)["text"], "fullscreen redraw flood finished")
    before = test.ctl("server", "info")
    test.save("before-reattach.json", test.content(pane))

    old_gui = test.gui
    test.ctl("ui", "menu", "quit-gui")
    assert old_gui.wait(timeout=5) == 0, "first GUI did not detach cleanly"
    test.log.close()
    test.log = (test.directory / "reattached-gui.log").open("w")
    env = dict(os.environ, WATER_TEST_INSTANCE=test.test_instance, WATER_TEST_UNFOCUSED="1",
               WATER_CONTROL_SOCKET=test.socket, WATER_CONFIG=str(test.directory / "config.json"))
    test.gui = subprocess.Popen([test.binary, "--control-socket", test.socket,
                                 "--config", str(test.directory / "config.json")],
                                stdout=test.log, stderr=subprocess.STDOUT, env=env)
    test.ownership.update(original_gui_pid=old_gui.pid, gui_pid=test.gui.pid)
    test.save("ownership.json", test.ownership)
    snapshot = test.wait(lambda: test.ctl("ui", "snapshot"), "reattached GUI", transient=True)
    test.window = snapshot["window_id"]
    test.wait(lambda: "WHEEL_REPLAY_READY" in test.content(pane)["text"], "replayed fullscreen frame")
    after = test.ctl("server", "info")
    assert after["instance_id"] == before["instance_id"], "reattach replaced the server"
    assert after["server_pid"] == before["server_pid"], "reattach restarted the server"
    test.ownership["window_id"] = test.window
    test.save("ownership.json", test.ownership)

    for delta, marker in ((-80, "WHEEL_RECEIVED_UP"), (80, "WHEEL_RECEIVED_DOWN")):
        snapshot = test.ctl("ui", "snapshot")
        grid = next(g for g in snapshot["terminal_grids"] if g["pane_id"] == pane)
        x0, y0, x1, y1 = grid["rect"]
        previous = test.content(pane)["last_seq"]
        test.ctl("ui", "wheel", "--x", (x0+x1)/2, "--y", (y0+y1)/2, "--dy", delta)
        result = test.wait(lambda: (lambda c: c if c["last_seq"] > previous and marker in c["text"] else None)
                           (test.content(pane)), "application received " + marker)
        assert result["alt_screen"] and result["y_disp"] == result["y_base"] == 0
        assert not result["selection"]["Active"], "wheel left a selection"
        test.save(marker + ".json", result)
    test.screenshot("reattached-wheel.png")
    print("PASS bounded fullscreen replay, GUI detach/reattach, up/down SGR wheel delivered to the running TUI; artifacts="
          + str(test.directory), flush=True)
