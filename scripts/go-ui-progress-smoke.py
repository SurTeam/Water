#!/usr/bin/env python3
"""Verify OSC progress projection and terminal control keys through Water ctl."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time

from PIL import Image

os.environ["WATER_TEST_UNFOCUSED"] = "1"

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
directory = Path(tempfile.mkdtemp(prefix="water-progress.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"startup": {"default_cwd": str(directory)},
                              "shell": {"program": shell, "args": ["-f"]},
                              "server": {"detached": False, "detach_on_quit": False}}))
log = (directory / "gui.log").open("w")
gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)],
                       stdout=log, stderr=subprocess.STDOUT)
(directory / "ownership.json").write_text(json.dumps({"gui_pid": gui.pid, "socket": socket}))


def ctl(*args):
    result = subprocess.run([water, "ctl", "--socket", socket, *map(str, args)],
                            capture_output=True, text=True, timeout=5)
    if result.returncode:
        raise RuntimeError(result.stderr)
    return json.loads(result.stdout)


def wait(predicate, description):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError((directory / "gui.log").read_text())
        try:
            value = predicate()
            if value:
                return value
        except (RuntimeError, subprocess.TimeoutExpired):
            pass
        time.sleep(.025)
    raise RuntimeError("Timeout: " + description + "; artifacts=" + str(directory))


try:
    pane = wait(lambda: ctl("ui", "snapshot").get("frame_focused_pane"), "GUI ready")
    # Exercise ordinary terminal controls in the real action/PTY path.
    ctl("ui", "key", "text:printf '%s\\n' WATER_KEY_OKX")
    ctl("ui", "key", "Backspace")
    ctl("ui", "key", "Return")
    wait(lambda: any(line.strip() == "WATER_KEY_OK" for line in
                     ctl("pane", "content", "--pane", pane).get("lines", [])),
         "one-character deletion and Return")
    # A harmless named foreground probe reports progress on command, without
    # calling a model, installing hooks or depending on an installed agent CLI.
    probe = "while IFS= read -r state; do printf '\\033]9;4;%s\\007' \"$state\"; done"
    ctl("pane", "input", "--pane", pane, "--text",
        "exec -a pi " + shlex.quote(shell) + " -f -c " + shlex.quote(probe) + "\n")

    def status():
        return next((a["status"] for a in ctl("ui", "snapshot")["sidebar_agents"]
                     if a["pane_id"] == pane), None)

    wait(lambda: status() == "Running", "foreground probe detection")
    icon_sizes = {}
    for report, expected in (("4;25", "Paused"), ("3", "Running"),
                             ("2;50", "Error"), ("0", "Idle"), ("1;75", "Running")):
        ctl("pane", "input", "--pane", pane, "--text", report + "\n")
        wait(lambda: status() == expected, "OSC status " + expected)
        snapshot = ctl("ui", "snapshot")
        hit = next(h for h in snapshot["automation_hits"] if h["kind"] == "agent" and h["id"] == pane)
        x0, y0, x1, y1 = hit["rect"]
        scale = snapshot["display_scale"]
        pad = min(round(snapshot["effective_config"]["ui"]["sidebar_agent_row_padding"] * scale), (x1-x0)//8)
        path = directory / (expected.lower() + "-icon.png")
        ctl("ui", "screenshot", "--output", path)
        image = Image.open(path).convert("RGB").crop((x0+pad, y0, x0+pad+round(16*scale), y1))
        background = image.getpixel((0, 0))
        ink = [(x, y) for y in range(image.height) for x in range(image.width)
               if max(abs(a-b) for a, b in zip(image.getpixel((x, y)), background)) > 30]
        assert ink, "missing status icon: " + expected
        width = max(x for x, y in ink) - min(x for x, y in ink) + 1
        height = max(y for x, y in ink) - min(y for x, y in ink) + 1
        icon_sizes[expected] = (width, height)
    assert max(v[0] for v in icon_sizes.values()) - min(v[0] for v in icon_sizes.values()) <= 1, icon_sizes
    assert max(v[1] for v in icon_sizes.values()) - min(v[1] for v in icon_sizes.values()) <= 1, icon_sizes
    (directory / "icon-sizes.json").write_text(json.dumps(icon_sizes))
    ctl("ui", "screenshot", "--output", directory / "progress.png")
    ctl("terminal", "spawn", "--pane", pane, "--program", shell, "--", "-f")
    wait(lambda: not ctl("ui", "snapshot")["sidebar_agents"], "stopped probe cleared")
    print("PASS OSC Running/Paused/Error/Idle with equal rendered icon bounds " + str(icon_sizes) + ", stopped probe, Enter/Backspace; artifacts=" + str(directory))
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
        gui.wait()
    log.close()
