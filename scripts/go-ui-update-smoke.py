#!/usr/bin/env python3
"""Exercise the real update panel and native action without installing an update."""
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/update-smoke/water-dev"))
directory = Path(tempfile.mkdtemp(prefix="water-update.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh", "/usr/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"shell": {"program": shell, "args": ["-f"]},
                             "server": {"detached": False}, "ui": {"language": "zh-Hans"}}))
gui = None
log = (directory / "gui.log").open("w")


def ctl(*args):
    result = subprocess.run([water, "ctl", "--socket", socket, *args], capture_output=True, timeout=5)
    if result.returncode:
        raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout)


def wait(predicate, description, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError("GUI exited: " + description)
        try:
            state = ctl("ui", "snapshot")
            if predicate(state):
                return state
        except RuntimeError:
            pass
        # Poll only within the bounded readiness condition.
        time.sleep(.025)
    raise RuntimeError("Timeout: " + description + "; artifacts=" + str(directory))


def click(label):
    state = wait(lambda s: any(h.get("label") == label for h in s["automation_hits"]), label)
    hit = next(h for h in state["automation_hits"] if h.get("label") == label)
    x0, y0, x1, y1 = hit["rect"]
    assert ctl("ui", "click", "--x", str((x0 + x1) / 2), "--y", str((y0 + y1) / 2))["handled"]


try:
    gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], stdout=log, stderr=subprocess.STDOUT)
    print(f"owned_gui_pid={gui.pid} socket={socket}", flush=True)
    initial = wait(lambda s: s.get("native_menu", {}).get("ready") and s.get("update", {}).get("phase") == "idle", "update startup")
    assert not initial["update_restart_allowed"]
    assert ctl("ui", "key", "cmd-,")["handled"]
    wait(lambda s: s.get("settings_visible"), "settings")
    click("update:open")
    wait(lambda s: s.get("update_visible"), "software update panel")
    assert ctl("ui", "key", "text:WATER_UPDATE_KEY_MUST_NOT_LEAK")["handled"]
    ctl("ui", "screenshot", "--output", str(directory / "checking.png"))
    complete = wait(lambda s: s["update"]["phase"] in ("current", "available", "error"), "bounded update check", timeout=25)
    print("update_result=" + json.dumps(complete["update"], ensure_ascii=False), flush=True)
    # A completed check must expose an actual clickable next action.
    assert any(h.get("label") in ("update:check", "update:download") for h in complete["automation_hits"])
    ctl("ui", "screenshot", "--output", str(directory / "result.png"))
    click("update:close")
    wait(lambda s: not s["update_visible"] and s["settings_visible"], "return to unchanged settings")
    assert ctl("ui", "key", "escape")["handled"]
    wait(lambda s: not s["settings_visible"], "close settings")
    ctl("ui", "menu", "check-updates")
    menu = wait(lambda s: s["update_visible"], "real native update action")
    if platform.system() == "Darwin":
        assert menu["native_menu"]["items"]["check-updates"]["title"] == "软件更新"
    assert ctl("ui", "key", "escape")["handled"]
    wait(lambda s: not s["update_visible"], "Escape closes update")
    print("PASS: settings entry, localized native menu, check completion, modal input and screenshots; artifacts=" + str(directory))
finally:
    if gui is not None:
        if gui.poll() is None:
            try:
                ctl("ui", "menu", "quit-and-server")
                gui.wait(timeout=5)
            except Exception:
                # Only this test's recorded process may be terminated.
                gui.terminate()
                gui.wait(timeout=5)
        else:
            gui.wait()
    log.close()
