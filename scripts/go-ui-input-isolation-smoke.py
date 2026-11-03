#!/usr/bin/env python3
"""Verify GUI control and neighboring panes while a child never reads stdin."""
import shlex
import subprocess
import sys
import time

from water_test import WaterGUI


with WaterGUI("water-input-isolation.") as test:
    def snapshot():
        return test.ctl("ui", "snapshot")

    blocked = snapshot()["frame_focused_pane"]
    test.ctl("ui", "key", "cmd-\\")
    test.wait(lambda: len(snapshot()["terminal_grids"]) == 2, "split")
    quiet = snapshot()["frame_focused_pane"]
    pid_file = test.directory / "blocked.pid"
    source = "import os,time; open(" + repr(str(pid_file)) + ", 'w').write(str(os.getpid())); print('INPUT_BLOCKED_READY', flush=True); time.sleep(120)"
    command = shlex.join([sys.executable, "-c", source])
    test.ctl("pane", "input", "--pane", blocked, "--text", command + "\n")
    test.wait(lambda: pid_file.exists() and "INPUT_BLOCKED_READY" in test.content(blocked)["text"], "non-reading child")
    grid = next(grid for grid in snapshot()["terminal_grids"] if grid["pane_id"] == blocked)
    left, top, right, bottom = grid["rect"]
    test.ctl("ui", "click", "--x", (left + right) // 2, "--y", (top + bottom) // 2)
    for _ in range(24):
        test.ctl("ui", "key", "text:" + "x" * 65536)
    started = time.monotonic()
    grid = next(grid for grid in snapshot()["terminal_grids"] if grid["pane_id"] == quiet)
    left, top, right, bottom = grid["rect"]
    test.ctl("ui", "click", "--x", (left + right) // 2, "--y", (top + bottom) // 2)
    test.ctl("ui", "key", "text:printf 'INPUT_ISOLATION_%s\\n' OK")
    test.ctl("ui", "key", "Return")
    test.wait(lambda: "INPUT_ISOLATION_OK" in test.content(quiet)["text"], "neighbor echo")
    before = len(test.ctl("state")["workspaces"])
    test.ctl("workspace", "new")
    test.wait(lambda: len(test.ctl("state")["workspaces"]) == before + 1, "workspace creation")
    memory = test.ctl("debug", "memory")
    child_pid = int(pid_file.read_text())
    close_started = time.monotonic()
    test.ctl("pane", "close", "--pane", blocked)
    test.wait(lambda: test.ctl("debug", "memory")["terminal_count"] == memory["terminal_count"] - 1, "blocked PTY removed")
    def child_exited():
        result = subprocess.run(["ps", "-p", str(child_pid), "-o", "stat="], capture_output=True, text=True, timeout=3)
        return result.returncode != 0 or result.stdout.strip().startswith("Z")
    test.wait(child_exited, "blocked child exited")
    test.save("result.json", {"neighbor_roundtrip_seconds": close_started - started,
                              "blocked_pane_close_seconds": time.monotonic() - close_started})
    print("PASS blocked-input GUI and pane isolation; artifacts=" + str(test.directory))
