#!/usr/bin/env python3
"""Exercise shared-server windows through Water's real control API."""
import argparse
import json
import pathlib
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--embedded", action="store_true")
    args = parser.parse_args()
    binary = str(pathlib.Path(args.binary).resolve())
    root = pathlib.Path(tempfile.mkdtemp(prefix="water-multi-", dir="/tmp"))
    socket = root / "control.sock"
    config = root / "config.json"
    config.write_text(json.dumps({"server": {"detached": not args.embedded,
        "auto_start": True, "detach_on_quit": False},
        "shell": {"program": "/opt/homebrew/bin/zsh" if pathlib.Path("/opt/homebrew/bin/zsh").exists() else "/bin/zsh", "args": ["-f"]}}))
    log = (root / "gui.log").open("w")
    gui = subprocess.Popen([binary, "--control-socket", str(socket), "--config", str(config)], stdout=log, stderr=log)

    def ctl(*words):
        proc = subprocess.run([binary, "ctl", "--socket", str(socket), *words], capture_output=True, text=True, timeout=7)
        if proc.returncode:
            raise RuntimeError(proc.stderr.strip())
        output = proc.stdout.strip()
        if not output or output == "pong": return output
        return json.loads(output)

    def wait(fn, description):
        deadline = time.monotonic() + 7
        last = None
        while time.monotonic() < deadline:
            try:
                value = fn()
                if value:
                    return value
            except (RuntimeError, KeyError) as exc:
                last = exc
            time.sleep(.02)
        raise AssertionError(f"timeout: {description}; {last}")

    windows = []
    def matching(value, predicate):
        return value if predicate(value) else None
    try:
        wait(lambda: ctl("ping"), "GUI ready")
        info = wait(lambda: matching(ctl("server", "info"), lambda i: i["ui_sessions"] == 1), "first session")
        server_pid = info["server_pid"]
        a = info["windows"][0]["window_id"]
        windows.append(a)
        original = wait(lambda: matching(ctl("ui", "state", "--window", a), lambda u: u["frame_active_workspace"] != str(uuid.UUID(int=0))), "first frame")
        ctl("ui", "key", "Cmd-N", "--window", a)
        info = wait(lambda: matching(ctl("server", "info"), lambda i: i["ui_sessions"] == 2), "Cmd+N second session")
        assert info["server_pid"] == server_pid
        b = next(w["window_id"] for w in info["windows"] if w["window_id"] != a)
        windows.append(b)
        ctl("ui", "key", "Cmd-Shift-N", "--window", b)
        second = wait(lambda: matching(ctl("ui", "state", "--window", b), lambda u: u["frame_active_workspace"] != original["frame_active_workspace"]), "independent workspace")
        assert ctl("ui", "state", "--window", a)["frame_active_workspace"] == original["frame_active_workspace"]
        # A background window must route creation to its own workspace/pane.
        ctl("ui", "key", "Cmd-T", "--window", a)
        wait(lambda: ctl("ui", "state", "--window", a)["frame_focused_pane"] != original["frame_focused_pane"], "tab in first workspace")
        assert ctl("ui", "state", "--window", b)["frame_focused_pane"] == second["frame_focused_pane"]
        # Same PTY, different window geometry; focus determines its shared size.
        ctl("ui", "key", "Cmd-Shift-N", "--window", a)
        shared = wait(lambda: matching(ctl("ui", "state", "--window", a), lambda u: u["frame_active_workspace"] != original["frame_active_workspace"]), "shared workspace")
        hit = next(h for h in ctl("ui", "state", "--window", b)["automation_hits"] if h["kind"] == "workspace" and h["id"] == shared["frame_active_workspace"])
        x0, y0, x1, y1 = hit["rect"]
        ctl("ui", "click", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2), "--window", b)
        wait(lambda: ctl("ui", "state", "--window", b)["frame_active_workspace"] == shared["frame_active_workspace"], "shared terminal visible")
        ctl("ui", "menu", "show-window", "--window", a)
        wait(lambda: ctl("ui", "state", "--window", a)["window_focused"], "first focus")
        before = ctl("ui", "state", "--window", a)
        hit = next(h for h in before["automation_hits"] if h["kind"] == "window_maximize")
        x0, y0, x1, y1 = hit["rect"]
        ctl("ui", "click", "--x", str((x0+x1)/2), "--y", str((y0+y1)/2), "--window", a)
        larger = wait(lambda: matching(ctl("ui", "state", "--window", a), lambda u: u["window_size"] != before["window_size"]), "different geometry")
        large_cols = larger["terminal_grids"][0]["columns"]
        wait(lambda: ctl("ui", "state", "--window", b)["terminal_grids"][0]["terminal_columns"] == large_cols, "background adopts focused PTY size")
        ctl("ui", "menu", "show-window", "--window", b)
        wait(lambda: ctl("ui", "state", "--window", b)["window_focused"], "second focus")
        smaller = wait(lambda: matching(ctl("ui", "state", "--window", b), lambda u: u["terminal_grids"][0]["terminal_columns"] == u["terminal_grids"][0]["columns"] and u["terminal_grids"][0]["terminal_columns"] != large_cols), "focus resizes PTY")
        small_cols = smaller["terminal_grids"][0]["columns"]
        wait(lambda: ctl("ui", "state", "--window", a)["terminal_grids"][0]["terminal_columns"] == small_cols, "old window adopts new size")
        ctl("ui", "menu", "quit-gui", "--window", a)
        wait(lambda: ctl("server", "info")["ui_sessions"] == 1, "first window released")
        assert ctl("ping") == "pong"
        assert ctl("server", "info")["server_pid"] == server_pid
        ctl("ui", "menu", "quit-gui", "--window", b)
        wait(lambda: not socket.exists(), "last window stops server")
        gui.wait(timeout=7)
        print(json.dumps({"passed": True, "embedded": args.embedded, "gui_pid":gui.pid, "server_pid":server_pid, "windows":windows, "focused_columns":[large_cols,small_cols], "artifacts":str(root)}))
    finally:
        if socket.exists():
            try:
                for window in ctl("server", "info")["windows"]:
                    ctl("ui", "menu", "quit-gui", "--window", window["window_id"])
                ctl("server", "shutdown")
            except RuntimeError:
                pass
        if gui.poll() is None:
            gui.terminate()  # Only the Popen process created by this test.
            gui.wait(timeout=7)
        log.close()


if __name__ == "__main__":
    main()
