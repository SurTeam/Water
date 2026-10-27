#!/usr/bin/env python3
"""Verify pane isolation, workspace creation and close under sustained output."""
import json
import shlex
import sys
import subprocess
import time
from water_test import WaterGUI

with WaterGUI("water-output-isolation.", config={"startup": {"window_columns": 120, "window_rows": 40}}) as test:
    def snapshot():
        return test.ctl("ui", "snapshot")

    def grids():
        return snapshot()["terminal_grids"]

    def focus(pane):
        g = next(g for g in grids() if g["pane_id"] == pane)
        x0, y0, x1, y1 = g["rect"]
        test.ctl("ui", "click", "--x", (x0+x1)//2, "--y", (y0+y1)//2)
        test.wait(lambda: snapshot()["frame_focused_pane"] == pane, "focus pane")

    a = snapshot()["frame_focused_pane"]
    terminal_a = snapshot()["frame_active_terminal"]
    assert test.ctl("ui", "key", "cmd-\\")["handled"]
    test.wait(lambda: len(grids()) == 2, "split")
    b = snapshot()["frame_focused_pane"]
    # Finish a large directory-shaped burst before testing sustained output.
    burst = "import sys;sys.stdout.write(('\\x1b[32m文件 entry\\x1b[0m\\r\\n'*500000)+'BURST_COMPLETE\\r\\n')"
    test.ctl("pane", "input", "--pane", a, "--text", shlex.join([sys.executable, "-c", burst])+"\n")
    test.wait(lambda: "BURST_COMPLETE" in test.content(a)["text"], "large burst fully parsed", timeout=20)
    # A continuous ANSI/Unicode stream at roughly 4 MiB/s, with no finite
    # completion to accidentally make the other-pane assertions pass when idle.
    emitter = test.save("emit.py", "import os,time\nopen('emitter.pid','w').write(str(os.getpid()))\nchunk=('\\x1b[32m海量目录 output\\x1b[0m\\r\\n'*1800).encode()\nwhile True:\n os.write(1,chunk)\n time.sleep(len(chunk)/(4*1024*1024))\n")
    test.ctl("pane", "input", "--pane", a, "--text", shlex.join([sys.executable, str(emitter)])+"\n")
    test.wait(lambda: "海量目录" in test.content(a)["text"], "flood visible")
    latency = []
    for n in range(8):
        started = time.monotonic()
        focus(b)
        test.ctl("ui", "key", "text:printf 'ISOLATION_%s\\n' "+str(n))
        test.ctl("ui", "key", "Return")
        test.wait(lambda: "ISOLATION_"+str(n) in test.content(b)["text"], "other pane echo")
        latency.append(round((time.monotonic()-started)*1000, 2))
    # Model creation exercises the same workspace command used by native opens.
    before = len(test.ctl("state")["workspaces"])
    started = time.monotonic()
    test.ctl("workspace", "new")
    test.wait(lambda: len(test.ctl("state")["workspaces"]) == before+1 and len(grids()) == 1, "new workspace during flood")
    fresh = snapshot()["frame_focused_pane"]
    test.ctl("ui", "key", "text:printf 'WORKSPACE_%s\\n' READY")
    test.ctl("ui", "key", "Return")
    test.wait(lambda: "WORKSPACE_READY" in test.content(fresh)["text"], "new workspace shell")
    workspace_ms = round((time.monotonic()-started)*1000, 2)
    before_close = test.ctl("debug", "memory")
    emitter_pid = int((test.directory / "emitter.pid").read_text())
    # Select the original workspace by its owning pane, then use the real close shortcut.
    state = test.ctl("state")
    def has_pane(node):
        if not isinstance(node, dict):
            return False
        return node.get("pane_id") == a or has_pane(node.get("first")) or has_pane(node.get("second"))
    original = next(ws for ws in state["workspaces"] if any(has_pane(tab["tree"]) for tab in ws["tabs"]))
    test.ctl("workspace", "activate", "--workspace", original["id"])
    test.wait(lambda: len(grids()) == 2, "return original workspace")
    focus(a)
    started = time.monotonic()
    test.ctl("ui", "key", "cmd-shift-w")
    test.wait(lambda: len(grids()) == 1 and grids()[0]["pane_id"] == b, "close flooding pane")
    close_ms = round((time.monotonic()-started)*1000, 2)
    test.ctl("ui", "key", "text:printf 'AFTER_CLOSE_%s\\n' OK")
    test.ctl("ui", "key", "Return")
    test.wait(lambda: "AFTER_CLOSE_OK" in test.content(b)["text"], "remaining pane after close")
    def emitter_exited():
        result = subprocess.run(["ps", "-p", str(emitter_pid), "-o", "stat="], capture_output=True, text=True, timeout=3)
        return result.returncode != 0 or result.stdout.strip().startswith("Z")
    test.wait(emitter_exited, "closed pane child process exit")
    test.wait(lambda: test.ctl("debug", "memory")["terminal_count"] == before_close["terminal_count"]-1, "closed PTY removed")
    assert test.ctl("debug", "memory")["exited_terminal_count"] == 0
    test.save("result.json", {"pane_roundtrip_ms": latency, "workspace_ms": workspace_ms, "close_ms": close_ms, "closed_terminal": terminal_a, "memory": test.ctl("debug", "memory")})
    test.screenshot("remaining-pane.png")
    print("PASS output isolation, workspace and shortcut close; roundtrip_ms="+json.dumps(latency)+" workspace_ms="+str(workspace_ms)+" close_ms="+str(close_ms)+" artifacts="+str(test.directory), flush=True)
