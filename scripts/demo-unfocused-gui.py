#!/usr/bin/env python3
"""Launch a visible Water test GUI without taking macOS focus.

Run on macOS with a built target/go-ui-smoke/water. The script prints the
frontmost app while it sends a short command through Water's own control API,
then keeps the window open for inspection until Ctrl+C.
"""
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
WATER = Path(os.environ.get("WATER_BIN", ROOT / "target/go-ui-smoke/water")).resolve()
FRONTMOST_SOURCE = r'''import AppKit
if let app = NSWorkspace.shared.frontmostApplication {
    print("\(app.processIdentifier)\t\(app.localizedName ?? "?")")
} else {
    print("?\t?")
}
'''


def build_frontmost_probe(directory):
    source = directory / "frontmost.swift"
    binary = directory / "frontmost"
    source.write_text(FRONTMOST_SOURCE)
    subprocess.run(["swiftc", "-o", str(binary), str(source)], check=True, timeout=30)
    return binary


def ctl(binary, socket, *args):
    result = subprocess.run([str(binary), "ctl", "--socket", socket, *map(str, args)],
                            capture_output=True, text=True, timeout=5)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return {"text": result.stdout.strip()}


def main():
    if os.uname().sysname != "Darwin":
        raise SystemExit("This focus demo requires macOS.")
    if not WATER.is_file():
        raise SystemExit(f"Water GUI binary not found: {WATER}\nBuild it first: go build -o target/go-ui-smoke/water ./cmd/water")

    directory = Path(tempfile.mkdtemp(prefix="water-unfocused-demo.", dir="/tmp"))
    socket = str(directory / "control.sock")
    config_path = directory / "config.json"
    probe = build_frontmost_probe(directory)
    shell = next((p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).is_file()), None)
    if shell is None:
        raise SystemExit("zsh is required for the GUI test.")
    config_path.write_text(json.dumps({
        "startup": {"default_cwd": str(directory), "window_width": 900, "window_height": 560},
        "shell": {"program": shell, "args": ["-f"]},
        "server": {"detached": False, "detach_on_quit": False},
    }))
    log = (directory / "gui.log").open("w")
    instance = "demo-" + uuid.uuid4().hex[:8]
    env = dict(os.environ, WATER_TEST_INSTANCE=instance,
               WATER_TEST_UNFOCUSED="1", WATER_CONTROL_SOCKET=socket,
               WATER_CONFIG=str(config_path))
    env.pop("WATER_SOCKET", None)
    env.pop("WATER_GO_DAEMON_CHILD", None)
    gui = subprocess.Popen([str(WATER), "--control-socket", socket, "--config", str(config_path)],
                           stdout=log, stderr=subprocess.STDOUT, env=env)
    server_pid = None
    was_interrupted = False

    def frontmost():
        result = subprocess.run([str(probe)], capture_output=True, text=True, timeout=3, check=True)
        fields = result.stdout.strip().split("\t", 1)
        return (fields[0], fields[1] if len(fields) > 1 else "?")

    def wait_for(predicate, description, timeout=12):
        deadline = time.monotonic() + timeout
        last_error = None
        while time.monotonic() < deadline:
            if gui.poll() is not None:
                raise RuntimeError(f"GUI exited while waiting for {description}; log={directory / 'gui.log'}")
            try:
                value = predicate()
                if value:
                    return value
            except (RuntimeError, subprocess.TimeoutExpired) as error:
                last_error = error
            time.sleep(0.05)
        raise RuntimeError(f"timeout waiting for {description}; last_error={last_error}; artifacts={directory}")

    def on_interrupt(signum, frame):
        nonlocal was_interrupted
        was_interrupted = True
        raise KeyboardInterrupt

    old_sigint = signal.signal(signal.SIGINT, on_interrupt)
    print(f"GUI PID: {gui.pid}; instance: {instance}", flush=True)
    print(f"Artifacts/config/socket: {directory}", flush=True)
    try:
        before_pid, before_name = frontmost()
        print(f"Frontmost before launch: {before_name} (pid {before_pid})", flush=True)
        snap = wait_for(lambda: (lambda s: s if s.get("frame_focused_pane") else None)(
            ctl(WATER, socket, "ui", "snapshot")), "first GUI frame")
        server_info = ctl(WATER, socket, "server", "info")
        server_pid = server_info["server_pid"]
        if server_info.get("socket_path") != socket or server_info.get("build_variant") != "dev":
            raise RuntimeError("refusing to operate on a server outside this demo's socket/dev variant")
        pane = snap["frame_focused_pane"]
        current_pid, current_name = frontmost()
        focused = snap.get("window_focused")
        print(f"After launch: frontmost={current_name} (pid {current_pid}); "
              f"GUI window_focused={focused}; pane={pane}", flush=True)
        if current_pid == str(gui.pid):
            print("FAIL: Water became the frontmost application.", flush=True)
        else:
            print("PASS: Water did not become the frontmost application.", flush=True)
        if focused is not False:
            print("FAIL: GUI snapshot does not report an unfocused window.", flush=True)

        marker = "WATER_UNFOCUSED_DEMO_OK"
        command = ("for i in 1 2 3; do printf '\\r  [%s/3] %s%% ' $i $((i*33)); "
                   f"sleep 1; done; printf '\\n{marker}\\n'")
        ctl(WATER, socket, "pane", "input", "--pane", pane, "--text", command)
        ctl(WATER, socket, "ui", "key", "Return")
        deadline = time.monotonic() + 9
        found = False
        while time.monotonic() < deadline:
            content = ctl(WATER, socket, "ui", "content", "--pane", pane,
                          "--window", snap["window_id"])
            if marker in content.get("text", ""):
                found = True
                break
            time.sleep(0.1)
        if not found:
            raise RuntimeError(f"command marker not found; artifacts={directory}")
        print(f"PASS: control API ran the 3-second progress command while unfocused; marker={marker}.", flush=True)
        final_pid, final_name = frontmost()
        print(f"After command: frontmost={final_name} (pid {final_pid})", flush=True)
        if final_pid == str(gui.pid):
            print("FAIL: Water took frontmost focus during the command.", flush=True)
        else:
            print("PASS: frontmost app remained outside this test GUI.", flush=True)

        ownership = {"gui_pid": gui.pid, "server_pid": server_pid, "socket": socket,
                     "config": str(config_path), "window_id": snap["window_id"],
                     "frontmost_before": {"pid": before_pid, "name": before_name},
                     "frontmost_after_launch": {"pid": current_pid, "name": current_name},
                     "frontmost_after_command": {"pid": final_pid, "name": final_name},
                     "window_focused": focused}
        (directory / "ownership.json").write_text(json.dumps(ownership, indent=2))
        print("\nGUI remains open and visible for inspection. Press Ctrl+C to close this demo instance.", flush=True)
        while gui.poll() is None:
            # Avoid repeated Swift process launches and control calls while
            # the user inspects the window; the initial and post-command
            # samples above are the focus evidence for this run.
            time.sleep(1.0)
    except KeyboardInterrupt:
        print("\nCtrl+C received; cleaning up this demo instance.", flush=True)
    finally:
        signal.signal(signal.SIGINT, old_sigint)
        try:
            info = ctl(WATER, socket, "server", "info")
            if info.get("socket_path") == socket and info.get("server_pid") == server_pid:
                if info.get("ui_sessions", 0) <= 1:
                    ctl(WATER, socket, "server", "shutdown")
        except (RuntimeError, subprocess.TimeoutExpired):
            pass
        if gui.poll() is None:
            gui.terminate()
        try:
            gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gui.kill()
            gui.wait(timeout=5)
        log.close()
        print(f"Demo GUI closed (pid {gui.pid}); evidence: {directory}", flush=True)
        if not was_interrupted and gui.returncode not in (0, -signal.SIGTERM):
            print(f"GUI exit code: {gui.returncode}; inspect {directory / 'gui.log'}", flush=True)


if __name__ == "__main__":
    main()
