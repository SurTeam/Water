#!/usr/bin/env python3
"""Refresh README images from an owned Water GUI using real control actions."""
import os
from pathlib import Path
import shlex
import shutil

from water_test import WaterGUI

root = Path(__file__).resolve().parent.parent
binary = os.environ.get("WATER_BIN", str(root / "dist/Water.app/Contents/MacOS/water"))
variant = os.environ.get("WATER_TEST_VARIANT", "release")
version = (root / "VERSION").read_text().strip()
images = root / "assets/screenshots"
images.mkdir(parents=True, exist_ok=True)

with WaterGUI("water-readme.", binary=binary, variant=variant, test_instance="",
              config={"startup": {"window_columns": 100, "window_rows": 32}}) as test:
    def snapshot():
        return test.ctl("ui", "snapshot")

    def current_pane():
        return snapshot()["frame_focused_pane"]

    def send(pane, command, expected):
        before = test.content(pane)["last_seq"]
        test.ctl("pane", "input", "--pane", pane, "--text", command + "\n")
        test.wait(lambda: (lambda c: c["last_seq"] > before and expected in c["text"]
                           and c["text"].rstrip().endswith("❯"))(test.content(pane)), "shell command: " + command)

    def prepare(pane):
        command = ("cd " + shlex.quote(str(root)) + "; alias water=" + shlex.quote(binary)
                   + "; PROMPT='%F{cyan}%1~%f %F{green}❯%f '; RPROMPT=''; printf '\\033[2J\\033[H'")
        send(pane, command, "Water ❯")

    def capture(name):
        # Capture actual Draw after the GUI's client viewport contains all output.
        state = snapshot()
        assert state["connection_error"] == "" and state["window_focused"] is False
        assert not any(g["selection"]["Active"] for g in state["terminal_grids"])
        test.save(name + ".json", state)
        for g in state["terminal_grids"]:
            test.save(name + "-" + g["pane_id"] + ".json", test.content(g["pane_id"]))
        shutil.copyfile(test.screenshot(name + ".png"), images / (name + ".png"))

    first = current_pane()
    prepare(first)
    send(first, "water --version | grep -E 'build_variant|client_version'", version)
    send(first, "go version", "go version")
    send(first, "ls cmd", "water-update")
    send(first, "ls -1 internal", "goterminal")
    capture("water-terminal-content")

    # Give each pane a useful real shell view, with lines short enough to remain
    # readable after the split. This reads the checkout without modifying it.
    send(first, "printf '\\033[2J\\033[H'; sed -n '1,22p' go.mod", "module github.com/SurTeam/Water")
    assert test.ctl("ui", "key", "cmd-\\")["handled"]
    test.wait(lambda: len(snapshot()["terminal_grids"]) == 2, "horizontal split")
    second = current_pane()
    prepare(second)
    send(second, "water --version | grep -E 'build_variant|client_version'", version)
    send(second, "go env GOOS GOARCH", "arm64")
    send(second, "ls cmd", "water-server")
    assert test.ctl("ui", "key", "cmd--")["handled"]
    test.wait(lambda: len(snapshot()["terminal_grids"]) == 3, "vertical split")
    third = current_pane()
    prepare(third)
    send(third, "ls internal/gomodel", "model.go")
    send(third, "water ctl ping", "pong")
    capture("water-split")
    print("Updated README screenshots; artifacts=" + str(test.directory), flush=True)
