#!/usr/bin/env python3
"""Close-workspace GUI regression using owned native GUI and real loopback SSH."""
import json
import os
import platform
import subprocess
from pathlib import Path

from fixtures.openssh_server import OpenSSHServer
from water_test import ControlError, WaterGUI, water_processes


def hits(test):
    return test.ctl("ui", "snapshot")["automation_hits"]


def hit(test, kind, identity=None):
    return next((h for h in hits(test) if h["kind"] == kind and
                 (identity is None or h.get("id") == identity)), None)


def click(test, target):
    assert target, "missing GUI hit"
    x0, y0, x1, y1 = target["rect"]
    assert test.ctl("ui", "click", "--x", (x0+x1)/2, "--y", (y0+y1)/2)["handled"]


def key(test, spec):
    assert test.ctl("ui", "key", spec)["handled"], spec


def prompt(test):
    return hit(test, "close_workspace_confirm")


def ids(control):
    return [w["id"] for w in (control("state")["workspaces"] or [])]


def evidence(test, name):
    test.save(name + ".json", test.ctl("ui", "snapshot"))
    path = test.screenshot(name + ".png")
    assert path.stat().st_size > 1000


def local(binary):
    with WaterGUI("water-workspace-close.", binary=binary,
                  config={"startup": {"window_columns": 110, "window_rows": 36}}) as test:
        first = test.ctl("state")["workspaces"][0]["id"]
        key(test, "cmd-shift-n")
        test.wait(lambda: len(ids(test.ctl)) == 2, "second workspace")
        second = next(w for w in ids(test.ctl) if w != first)
        test.wait(lambda: test.ctl("ui", "snapshot")["frame_active_workspace"] == second, "second frame")
        close = test.wait(lambda: hit(test, "close_workspace", first), "visible close button")
        evidence(test, "01-sidebar-close")
        click(test, close)
        test.wait(lambda: prompt(test), "button confirmation")
        assert prompt(test)["id"] == first
        assert not any(h["kind"] in ("workspace", "pane", "new_workspace", "tab") for h in hits(test))
        # All unrelated shortcuts and text must be swallowed by the modal.
        key(test, "cmd-shift-n")
        key(test, "text:WATER_MODAL_MUST_NOT_TYPE")
        assert ids(test.ctl) == [first, second]
        evidence(test, "02-button-confirmation")
        click(test, test.wait(lambda: hit(test, "close_workspace_cancel"), "cancel button"))
        test.wait(lambda: not prompt(test), "button cancel")
        assert first in ids(test.ctl)
        test.ctl("workspace", "activate", "--workspace", second)
        test.wait(lambda: test.ctl("ui", "snapshot")["frame_active_workspace"] == second, "shortcut target frame")
        key(test, "ctrl-alt-w")
        test.wait(lambda: prompt(test), "shortcut confirmation")
        key(test, "escape")
        test.wait(lambda: not prompt(test), "escape cancel")
        assert second in ids(test.ctl)
        key(test, "ctrl-alt-w")
        test.wait(lambda: prompt(test), "pinned confirmation")
        test.ctl("workspace", "activate", "--workspace", first)
        test.wait(lambda: test.ctl("ui", "snapshot")["frame_active_workspace"] == first, "external activation while modal")
        assert prompt(test)["id"] == second
        evidence(test, "03-pinned-target")
        key(test, "return")
        test.wait(lambda: second not in ids(test.ctl), "pinned target closed")
        assert first in ids(test.ctl)
        # Remove its last tab through the public CLI to exercise truly empty close.
        state = test.ctl("state")
        for tab in state["workspaces"][0]["tabs"]:
            test.ctl("tab", "close", "--tab", tab["id"])
        test.wait(lambda: not test.ctl("state")["workspaces"][0]["tabs"], "empty workspace")
        test.wait(lambda: not prompt(test) and hit(test, "close_workspace", first), "empty workspace frame")
        evidence(test, "04-empty-workspace")
        click(test, hit(test, "close_workspace", first))
        test.wait(lambda: first not in ids(test.ctl), "empty workspace closes directly")
        assert not prompt(test)
        evidence(test, "05-empty-closed")
        test.save("assertions.json", {"button_cancel": True, "shortcut_escape": True,
                   "shortcut_confirm_fixed_uuid": second, "modal_blocks_actions": True,
                   "empty_direct_close": True})
        print("PASS native GUI local workspace closure; artifacts=" + str(test.directory), flush=True)


def cross_connection(binary):
    with OpenSSHServer() as ssh:
        ssh.existing_pids = set(water_processes())
        value = 14695981039346656037
        for byte in (ssh.alias + "|" + str(ssh.directory / "ssh_config")).encode():
            value = ((value ^ byte) * 1099511628211) & ((1 << 64) - 1)
        ssh.control = f"/tmp/water-go-ssh-dev-{os.geteuid()}-{value:016x}.ctl"
        with WaterGUI("water-workspace-close-remote.", binary=binary,
                      config={"startup": {"window_columns": 110, "window_rows": 40}}) as test:
            local_ids = ids(test.ctl)
            key(test, "cmd-alt-k")
            key(test, "text:" + ssh.alias)
            key(test, "enter")
            entry = test.wait(lambda: next((e for e in test.ctl("connections", "list")["connections"]
                              if e["kind"] == "remote" and e["status"] == "connected"), None),
                              "real remote connection", timeout=20)
            def remote(*args):
                result = subprocess.run([binary, "ctl", "--socket", entry["socket_path"], *map(str, args)],
                                        capture_output=True, text=True, timeout=5)
                if result.returncode:
                    raise ControlError(result.stderr or result.stdout)
                return json.loads(result.stdout)
            ssh.record_remote(remote("server", "info"))
            test.wait(lambda: remote("state").get("focused_pane"), "remote terminal")
            remote_id = remote("state")["workspaces"][0]["id"]
            connections = test.ctl("connections", "list")["connections"]
            local_entry = next(e for e in connections if e["kind"] == "local")
            click(test, test.wait(lambda: hit(test, "connection", local_entry["id"]), "local connection button"))
            test.wait(lambda: test.ctl("ui", "snapshot")["frame_active_workspace"] in local_ids, "local selected")
            # Close an inactive remote workspace from the local connection's sidebar.
            click(test, test.wait(lambda: hit(test, "close_workspace", remote_id), "remote close button"))
            test.wait(lambda: prompt(test), "remote confirmation")
            assert prompt(test)["id"] == remote_id
            assert test.ctl("ui", "snapshot")["frame_active_workspace"] == remote_id
            evidence(test, "06-remote-confirmation")
            click(test, prompt(test))
            test.wait(lambda: remote_id not in ids(remote), "remote workspace closed")
            assert ids(test.ctl) == local_ids, "cross-connection close mutated local model"
            evidence(test, "07-remote-closed")
            test.save("routing.json", {"remote_target": remote_id, "local_preserved": local_ids,
                                       "connection": entry})
            print("PASS real SSH owning-connection sidebar closure; artifacts=" + str(test.directory), flush=True)
        print("SSH artifacts=" + str(ssh.directory), flush=True)


def main():
    if platform.system() == "Linux" and not (os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY")):
        raise RuntimeError("native display is required")
    binary = os.environ.get("WATER_BIN", str(Path(__file__).resolve().parent.parent / "target/go-app/dev/water"))
    local(binary)
    cross_connection(binary)


if __name__ == "__main__":
    main()
