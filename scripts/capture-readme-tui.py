#!/usr/bin/env python3
"""Capture a real narrow TUI in an owned Water desktop terminal."""
import getpass
import json
import os
from pathlib import Path
import shlex
import subprocess

from water_test import WaterGUI

root = Path(__file__).resolve().parent.parent
binary = os.environ.get("WATER_BIN", str(root / "dist/Water.app/Contents/MacOS/water"))
variant = os.environ.get("WATER_TEST_VARIANT", "release")
with WaterGUI("water-readme-tui.", binary=binary, variant=variant,
              config={"startup": {"window_columns": 56, "window_rows": 18}}) as test:
    inner_socket = str(test.directory / "tui.sock")
    inner_config = test.directory / "tui.json"
    inner_config.write_text('{"shell":{"program":"/bin/zsh","args":["-f"]},"startup":{"default_cwd":' + json.dumps(str(root)) + '}}')
    def inner(*args):
        return subprocess.run([binary, "ctl", "--socket", inner_socket, *args],
                              capture_output=True, text=True, timeout=10)
    pane = test.ctl("ui", "snapshot")["frame_focused_pane"]
    test.content(pane)
    command = " ".join([shlex.quote(binary), "tui", "--socket", shlex.quote(inner_socket),
                        "--config", shlex.quote(str(inner_config))])
    try:
        test.ctl("pane", "input", "--pane", pane, "--text", command + "\n")
        test.wait(lambda: inner("ping").returncode == 0, "TUI backend")
        state = json.loads(inner("state").stdout)
        test.save("tui-state.json", state)
        # The new backend has one shell; address its actual pane identity.
        def find_pane(value):
            if isinstance(value, dict):
                if value.get("pane_id") and (value.get("terminal_id") or value.get("terminal")):
                    return value["pane_id"]
                for child in value.values():
                    found = find_pane(child)
                    if found:
                        return found
            elif isinstance(value, list):
                for child in value:
                    found = find_pane(child)
                    if found:
                        return found
        child = find_pane(state)
        assert child, state
        demo = "PROMPT='%F{cyan}Water%f %F{green}❯%f '; RPROMPT=''; clear; ls cmd; go version\n"
        result = inner("pane", "input", "--pane", child, "--text", demo)
        assert result.returncode == 0, result.stderr
        test.wait(lambda: "go version go" in test.content(pane)["text"], "live narrow TUI output")
        visible = test.content(pane)
        for private in (getpass.getuser(), str(Path.home()), "/Users/", "/home/"):
            assert private not in visible["text"], "Personal information in screenshot viewport"
        test.save("tui-viewport.json", visible)
        frame = test.ctl("ui", "snapshot")
        test.save("tui-frame.json", frame)
        destination = root / "assets/screenshots/water-tui-narrow.png"
        # Keep only the actual terminal viewport, removing the outer GUI chrome.
        from PIL import Image
        original = test.screenshot("tui-narrow.png")
        grid = next(g for g in frame["terminal_grids"] if g["pane_id"] == pane)
        assert grid["columns"] < 90
        with Image.open(original) as image:
            image.crop(tuple(grid["rect"])).save(destination)
        print("Screenshot: " + str(destination))
        print("Evidence: " + str(test.directory))
    finally:
        if Path(inner_socket).exists():
            result = inner("server", "shutdown")
            assert result.returncode == 0, result.stderr
