#!/usr/bin/env python3
"""Stress hidden terminals and reject a GUI that keeps redrawing them.

The owned GUI uses a private socket and config. It never signals an existing
Water process. Hidden panes emit cursor-addressed redraws at 30 Hz; the visible
pane stays idle. Parsing may use a little CPU, but it must not become a
full-window refresh loop.
"""

from water_test import hold_gui_lock
hold_gui_lock()

import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--water", type=Path, required=True)
    parser.add_argument("--hidden", type=int, default=4)
    parser.add_argument("--seconds", type=float, default=5)
    parser.add_argument("--max-cpu", type=float, default=15)
    args = parser.parse_args()
    if not 1 <= args.hidden <= 8 or not 3 <= args.seconds <= 10 or not 1 <= args.max_cpu <= 50:
        parser.error("hidden must be 1..8, seconds 3..10, and max-cpu 1..50")
    water = str(args.water.resolve())
    directory = Path(tempfile.mkdtemp(prefix="water-hidden-cpu.", dir="/tmp"))
    socket = str(directory / "control.sock")
    config = directory / "config.json"
    emitter = directory / "refresh.py"
    shell = next(str(p) for p in (Path("/opt/homebrew/bin/zsh"), Path("/bin/zsh")) if p.exists())
    config.write_text(json.dumps({
        "startup": {"window_columns": 80, "window_rows": 24, "initial_terminal": True},
        "server": {"detached": False},
        "shell": {"program": shell, "args": ["-f"]},
        "terminal": {"cursor_blink": False, "scrollback_lines": 1000},
    }))
    emitter.write_text(
        "import sys, time\n"
        "print('WATER_OUTPUT_LOAD_START', flush=True)\n"
        "end = time.monotonic() + float(sys.argv[1])\n"
        "while time.monotonic() < end:\n"
        "    sys.stdout.write('\\x1b[H\\x1b[2J\\x1b[31m' + ('WATER_REFRESH ' * 12) + '\\x1b[0m')\n"
        "    sys.stdout.flush()\n"
        "    time.sleep(0.033)\n"
    )
    env = dict(os.environ, WATER_CONTROL_SOCKET=socket, WATER_CONFIG=str(config), WATER_TEST_UNFOCUSED="1")
    log = (directory / "gui.log").open("w")
    gui = subprocess.Popen([water, "--config", str(config), "--control-socket", socket], env=env, stdout=log, stderr=log)

    def ctl(*command, timeout=8):
        result = subprocess.run([water, "ctl", "--socket", socket, *command], env=env, capture_output=True, text=True, timeout=timeout)
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        if not result.stdout.strip():
            return None
        return json.loads(result.stdout)

    def terminals(state):
        found = []

        def walk(node, tab):
            if not isinstance(node, dict):
                return
            if node.get("type") == "leaf":
                summary = node.get("terminal", {}).get("summary", {})
                if summary.get("terminal_id"):
                    found.append((tab, summary["terminal_id"]))
            for child in ("first", "second"):
                walk(node.get(child), tab)

        for workspace in state.get("workspaces", []):
            for tab in workspace.get("tabs", []):
                walk(tab.get("tree"), tab.get("id"))
        return found

    def cpu(pid):
        value = subprocess.check_output(["ps", "-p", str(pid), "-o", "time="], text=True).strip()
        parts = [float(part) for part in value.split(":")]
        return sum(part * 60 ** index for index, part in enumerate(reversed(parts)))

    try:
        deadline = time.monotonic() + 15
        visible = None
        first_tab = None
        while time.monotonic() < deadline:
            if gui.poll() is not None:
                raise RuntimeError(f"GUI exited before ready; see {directory / 'gui.log'}")
            try:
                state = ctl("state")
                pairs = terminals(state)
                if pairs:
                    first_tab, visible = pairs[0]
                    ctl("terminal", "contains", "--terminal", visible, "%", "--timeout-ms", "1000")
                    break
            except (RuntimeError, subprocess.TimeoutExpired):
                time.sleep(0.1)
        else:
            raise RuntimeError(f"GUI did not become ready; see {directory / 'gui.log'}")
        hidden = []
        for index in range(args.hidden):
            ctl("tab", "new", "--title", f"hidden-{index}")
            found = time.monotonic() + 8
            while time.monotonic() < found:
                pairs = terminals(ctl("state"))
                known = {terminal for _, terminal in hidden}
                known.add(visible)
                extra = [terminal for _, terminal in pairs if terminal not in known]
                if extra:
                    hidden.append((pairs[-1][0], extra[-1]))
                    ctl("terminal", "contains", "--terminal", extra[-1], "%", "--timeout-ms", "5000")
                    break
                time.sleep(0.05)
            else:
                raise RuntimeError(f"hidden terminal {index} did not start")
        ctl("tab", "activate", "--tab", first_tab)
        snapshot = ctl("ui", "snapshot")
        if snapshot.get("frame_active_terminal") != visible:
            raise RuntimeError(f"visible terminal changed: {snapshot.get('frame_active_terminal')}")
        duration = args.seconds + 3
        command = shlex.join([str(Path.home() / ".venv/bin/python"), str(emitter), str(duration)]) + "\n"
        for _, terminal in hidden:
            ctl("terminal", "send", "--terminal", terminal, command)
            ctl("terminal", "contains", "--terminal", terminal, "WATER_OUTPUT_LOAD_START", "--timeout-ms", "5000")
        started = time.monotonic()
        before = cpu(gui.pid)
        time.sleep(args.seconds)
        if gui.poll() is not None:
            raise RuntimeError("GUI exited during the stress window")
        percent = (cpu(gui.pid) - before) / (time.monotonic() - started) * 100
        snapshot = ctl("ui", "snapshot")
        report = {
            "directory": str(directory),
            "gui_pid": gui.pid,
            "cpu_percent": round(percent, 2),
            "max_cpu": args.max_cpu,
            "hidden_terminals": len(hidden),
            "visible_terminal": visible,
            "window_occluded": snapshot.get("window_occluded"),
            "application_hidden": snapshot.get("application_hidden"),
            "window_focused": snapshot.get("window_focused"),
            "connection_error": snapshot.get("connection_error"),
        }
        (directory / "report.json").write_text(json.dumps(report, indent=2))
        print(json.dumps(report))
        if snapshot.get("connection_error"):
            raise SystemExit(f"session failed during stress: {snapshot['connection_error']}")
        if percent > args.max_cpu:
            raise SystemExit(f"GUI CPU {percent:.2f}% exceeded {args.max_cpu:.0f}%")
    finally:
        if gui.poll() is None:
            gui.terminate()
            try:
                gui.wait(timeout=5)
            except subprocess.TimeoutExpired:
                gui.kill()
                gui.wait(timeout=5)
        log.close()


if __name__ == "__main__":
    main()
