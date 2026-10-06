#!/usr/bin/env python3
"""Exercise capability diagnostics and guarded layout restart on local and SSH connections."""
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from water_test import WaterGUI, ControlError

if platform.system() == "Linux" and not (os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY")):
    raise RuntimeError("native display is required")
root = Path(__file__).resolve().parent.parent
binary = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
server_binary = os.environ.get("WATER_SERVER_BIN", str(root / "target/go-ui-smoke/water-server"))

def endpoint_ctl(socket, *args):
    result = subprocess.run([binary, "ctl", "--socket", socket, *map(str, args)], capture_output=True, text=True, timeout=5)
    if result.returncode:
        raise ControlError(result.stderr.strip() or result.stdout.strip())
    if args == ("server", "shutdown"):
        return {}
    return json.loads(result.stdout)

def click(test, label):
    hit = test.wait(lambda: next((h for h in test.ctl("ui", "snapshot").get("automation_hits", []) if h.get("label") == label), None), "click " + label)
    x0, y0, x1, y1 = hit["rect"]
    assert test.ctl("ui", "click", "--x", (x0+x1)/2, "--y", (y0+y1)/2)["handled"]

def project(state):
    def tree(node):
        if node["type"] == "split":
            return {"axis": node["axis"], "ratio": node["ratio"], "first": tree(node["first"]), "second": tree(node["second"])}
        return {"pane": node["pane_id"], "kind": node["surface_kind"]}
    return [{"id": w["id"], "title": w["title"], "tabs": [{"id": t["id"], "title": t["title"], "title_override": t.get("title_override"), "tree": tree(t["tree"])} for t in w["tabs"]]} for w in state["workspaces"]]

def owning_tab_id(state, pane):
    for workspace in state["workspaces"]:
        for tab in workspace["tabs"]:
            try:
                next_leaf({"workspaces": [{"tabs": [tab]}]}, pane)
                return tab["id"]
            except AssertionError:
                pass
    raise RuntimeError("GUI pane not present in the server layout")


def exercise(test, control, kind):
    state = control("state")
    pane = test.ctl("ui", "snapshot")["frame_focused_pane"]
    tab = owning_tab_id(state, pane)
    control("pane", "split", "--pane", pane, "--right")
    control("pane", "split", "--pane", pane, "--down")
    control("pane", "resize-split", "--tab", tab, "--ratio", ".31")
    control("workspace", "new")
    changed = test.directory / (kind + "-after-cd")
    changed.mkdir()
    control("pane", "input", "--pane", pane, "--text", "cd " + shlex.quote(str(changed)) + "; printf 'SERVER_%s\\n' CWD_READY\n")
    terminal = next_terminal(control("state"), pane)
    control("terminal", "contains", "--terminal", terminal, "SERVER_CWD_READY", "--timeout-ms", "3000")
    before = control("state")
    ui_before = test.ctl("ui", "snapshot")
    original = control("server", "info")
    if not ui_before["settings_visible"]:
        assert test.ctl("ui", "key", "cmd-,")["handled"]
    click(test, "category:Server")
    shown = test.wait(lambda: (lambda s: s if s.get("settings_category") == "Server" else None)(test.ctl("ui", "snapshot")), "Server tab")
    assert shown["server_status"]["compatibility"]["compatible"]
    assert shown["server_status"]["client"]["required_capabilities"]
    assert shown["server_status"]["server"]["required_capabilities"]
    assert "recovery/v1" in shown["server_status"]["server"]["capabilities"]
    test.save(kind + "-before.json", shown)
    test.screenshot(kind + "-server-tab.png")
    click(test, "server:inspect")
    test.wait(lambda: not test.ctl("ui", "snapshot")["server_status"]["busy"], "capability refresh")
    click(test, "server:backup")
    saved = test.wait(lambda: (lambda s: s if not s["busy"] and s["recovery_path"] else None)(test.ctl("ui", "snapshot")["server_status"]), "layout backup")
    assert saved["message"] == "Server operation completed", saved
    artifact = Path(saved["recovery_path"])
    document = json.loads(artifact.read_text())
    assert not document["pending"] and document["layout"]["schema"] == 1
    assert artifact.stat().st_mode & 0o077 == 0
    click(test, "server:restart")
    assert control("server", "info")["instance_id"] == original["instance_id"], "confirmation restarted server"
    test.screenshot(kind + "-restart-confirm.png")
    click(test, "server:restart")
    def restarted():
        try:
            snapshot = test.ctl("ui", "snapshot")
        except ControlError as error:
            # Local guarded shutdown intentionally removes this owned listener.
            # All other errors fail immediately, including remote UI errors.
            if kind == "local" and "UI_AUTOMATION_UNAVAILABLE: no GUI session is connected" in str(error):
                info = test.ctl("server", "info")
                if info["instance_id"] != original["instance_id"]:
                    test.record_server_info(info)
                    test.save("restart-listener.json", info)
                    return None  # GUI registration may race this metadata query.
            if kind == "local" and any(text in str(error).lower() for text in ("no such file", "connection refused", "eof", "connection reset")):
                return None
            raise
        status = snapshot["server_status"]
        if status["server"]["instance_id"] == original["instance_id"]:
            if not status["busy"] and status["message"] not in ("Server operation completed", "Working…"):
                raise RuntimeError(status["message"])
            return None
        assert not status["busy"] and status["compatibility"]["compatible"], status
        if snapshot.get("frame_focused_pane") != ui_before["frame_focused_pane"]:
            return None
        return snapshot
    after_ui = test.wait(restarted, kind + " guarded restart", timeout=15)
    test.window = after_ui["window_id"]
    # Remote operations replace the forwarding socket. Resolve it from the
    # connection list rather than continuing with the retired endpoint.
    if kind == "remote":
        entry = next(e for e in test.ctl("connections", "list")["connections"] if e["kind"] == "remote")
        control = lambda *args: endpoint_ctl(entry["socket_path"], *args)
    after = control("state")
    assert project(before) == project(after), (before, after)
    restored_tab = next(t for w in after["workspaces"] for t in w["tabs"] if t["id"] == tab)
    assert abs(restored_tab["tree"]["ratio"] - .31) < 1e-9, "ratio lost"
    assert after_ui["frame_focused_pane"] == ui_before["frame_focused_pane"], "window selection changed"
    assert next_terminal(after, pane) != terminal, "old PTY identity reused"
    restored_cwd = next_leaf(after, pane)["surface_state"]["Terminal"]["cwd"]
    assert Path(restored_cwd).resolve() == changed.resolve(), "current directory lost"
    assert not json.loads(artifact.read_text())["pending"], "recovery still pending"
    info = control("server", "info")
    if kind == "local":
        assert after_ui["update_restart_allowed"], "replacement detached server still blocks app updates"
        test.record_server_info(info)
    test.ownership[kind + "_replacement_server"] = {"pid": info["server_pid"], "instance": info["instance_id"], "window": test.window}
    test.save("ownership.json", test.ownership)
    test.save(kind + "-after.json", after_ui)
    test.screenshot(kind + "-restored.png")
    # Observe actual shell output after recovery, through the owning GUI view.
    control("pane", "input", "--pane", pane, "--text", "printf 'RESTORED_%s\\n' SHELL_READY; pwd\n")
    terminal = next_terminal(after, pane)
    control("terminal", "contains", "--terminal", terminal, "RESTORED_SHELL_READY", "--timeout-ms", "3000")
    def content_ready():
        current = control("ui", "content", "--pane", pane, "--window", test.window)
        text = "".join(line.rstrip() for line in current["lines"])
        test.save(kind + "-content-latest.json", current)
        return current if "RESTORED_SHELL_READY" in text and str(changed.resolve()) in text else None
    test.save(kind + "-content.json", test.wait(content_ready, "restored GUI shell output"))
    print("PASS " + kind + " server capability/backup/restart/live-CWD; artifacts=" + str(test.directory), flush=True)

def next_leaf(state, pane):
    def walk(node):
        if node["type"] == "leaf":
            return node if node["pane_id"] == pane else None
        return walk(node["first"]) or walk(node["second"])
    for w in state["workspaces"]:
        for tab in w["tabs"]:
            if result := walk(tab["tree"]):
                return result
    raise AssertionError("pane missing: " + pane)

def next_terminal(state, pane):
    return next_leaf(state, pane)["surface_state"]["Terminal"]["terminal_id"]

def main():
    with WaterGUI("water-server-local.") as test:
        exercise(test, test.ctl, "local")

    fixture = Path(tempfile.mkdtemp(prefix="water-server-ssh.", dir="/tmp"))
    remote_socket = str(fixture / "remote.sock")
    ssh = fixture / "ssh"
    ssh.write_text("#!/bin/sh\nexec " + shlex.quote(sys.executable) + " " + shlex.quote(str(root / "scripts/fixtures/server-ssh.py")) + ' "$@"\n')
    ssh.chmod(0o700)
    shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
    remote_config = fixture / "config.json"
    remote_config.write_text(json.dumps({"startup": {"default_cwd": str(fixture)}, "shell": {"program": shell, "args": ["-f"]}}))
    server = fixture / "server"
    server.write_text("#!/bin/sh\nexec " + shlex.quote(server_binary) + " --config " + shlex.quote(str(remote_config)) + ' "$@"\n')
    server.chmod(0o700)
    keys = {"WATER_SSH_PROGRAM": str(ssh), "WATER_SSH_FIXTURE_DIR": str(fixture), "WATER_REMOTE_CONTROL_SOCKET": remote_socket, "WATER_REMOTE_SERVER_COMMAND": str(server)}
    previous = {key: os.environ.get(key) for key in keys}
    os.environ.update(keys)
    try:
        with WaterGUI("water-server-remote.") as test:
            assert test.ctl("ui", "key", "cmd-shift-k")["handled"]
            assert test.ctl("ui", "key", "text:owned-ssh-fixture")["handled"]
            assert test.ctl("ui", "key", "enter")["handled"]
            entry = test.wait(lambda: next((e for e in test.ctl("connections", "list")["connections"] if e["kind"] == "remote" and e["status"] == "connected"), None), "owned SSH connection")
            control = lambda *args: endpoint_ctl(entry["socket_path"], *args)
            test.wait(lambda: control("state").get("focused_pane"), "remote initial workspace")
            test.wait(lambda: test.ctl("ui", "snapshot").get("frame_focused_pane") == control("state")["focused_pane"], "remote first frame")
            test.ownership["ssh_fixture"] = str(fixture)
            test.save("ownership.json", test.ownership)
            exercise(test, control, "remote")
    finally:
        cleanup_error = None
        if Path(remote_socket).exists():
            try:
                info = endpoint_ctl(remote_socket, "server", "info")
                recorded = {int(p) for p in (fixture / "server-pids.txt").read_text().splitlines()}
                command = subprocess.run(["ps", "-p", str(info["server_pid"]), "-o", "command="], capture_output=True, text=True, timeout=3).stdout
                if info["socket_path"] != remote_socket or info["server_pid"] not in recorded or str(remote_config) not in command:
                    raise RuntimeError("refusing remote cleanup: server ownership could not be proven")
                endpoint_ctl(remote_socket, "server", "shutdown")
                deadline = time.monotonic() + 3
                while Path(remote_socket).exists() and time.monotonic() < deadline:
                    time.sleep(.025)
                if Path(remote_socket).exists():
                    raise RuntimeError("owned remote server did not remove its socket")
            except BaseException as error:
                cleanup_error = error
        for record in fixture.glob("forward-*.json"):
            info = json.loads(record.read_text())
            try:
                os.kill(info["pid"], signal.SIGTERM)
            except ProcessLookupError:
                pass
        for key, value in previous.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        (fixture / "cleanup.json").write_text(json.dumps({"remote_socket_removed": not Path(remote_socket).exists(), "forwards_cancelled": True, "error": str(cleanup_error) if cleanup_error else None}))
        export = os.environ.get("WATER_TEST_EVIDENCE_DIR")
        if export:
            shutil.copytree(fixture, Path(export) / fixture.name, dirs_exist_ok=True)
        print("owned SSH fixture artifacts=" + str(fixture), flush=True)
        if cleanup_error:
            raise cleanup_error


if __name__ == "__main__":
    main()
