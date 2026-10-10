#!/usr/bin/env python3
"""Real OpenSSH authentication/deployment/forwarding/recovery in owned instances."""
import importlib.util
import json
import os
from pathlib import Path
import shlex
import time
from fixtures.openssh_server import OpenSSHServer
from water_test import WaterGUI, water_processes

os.environ["WATER_TEST_UNFOCUSED"] = "1"

spec = importlib.util.spec_from_file_location("water_server_smoke", Path(__file__).with_name("go-ui-server-smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def connect(test, alias):
    assert test.ctl("ui", "key", "cmd-shift-k")["handled"]
    assert test.ctl("ui", "key", "text:" + alias)["handled"]
    assert test.ctl("ui", "key", "enter")["handled"]
    entry = test.wait(lambda: next((e for e in test.ctl("connections", "list")["connections"]
                                   if e["kind"] == "remote" and e["status"] == "connected"), None),
                      "real SSH connection and payload deployment", timeout=20)
    control = lambda *args: smoke.endpoint_ctl(entry["socket_path"], *args)
    test.wait(lambda: control("state").get("focused_pane"), "remote workspace")
    test.wait(lambda: test.ctl("ui", "snapshot").get("frame_focused_pane") == control("state")["focused_pane"], "remote frame")
    return entry, control


def main():
    variant = os.environ.get("WATER_TEST_VARIANT", "dev")
    with WaterGUI("water-real-local.", variant=variant) as test:
        smoke.exercise(test, test.ctl, "local")
    with OpenSSHServer(variant=variant) as ssh:
        ssh.existing_pids = set(water_processes())
        value = 14695981039346656037
        for byte in (ssh.alias + "|" + str(ssh.directory / "ssh_config")).encode():
            value = ((value ^ byte) * 1099511628211) & ((1 << 64) - 1)
        ssh.control = f"/tmp/water-go-ssh-{variant}-{os.geteuid()}-{value:016x}.ctl"
        with WaterGUI("water-real-remote.", variant=variant) as test:
            entry, control = connect(test, ssh.alias)
            info = control("server", "info")
            ssh.record_remote(info)
            commands = (ssh.directory / "commands.log").read_text()
            assert "uname -s; uname -m" in commands and "cat >" in commands, "embedded deployment not exercised"
            test.save("real-ssh.json", {"alias": ssh.alias, "port": ssh.port, "sshd_pid": ssh.sshd.pid,
                                        "instance": info["instance_id"], "remote_socket": info["socket_path"],
                                        "default_payload_deployed": True})
            smoke.exercise(test, control, "remote")
            entry = next(e for e in test.ctl("connections", "list")["connections"] if e["kind"] == "remote")
            control = lambda *args: smoke.endpoint_ctl(entry["socket_path"], *args)
            restored = control("server", "info")
            ssh.record_remote(restored)
            before = smoke.project(control("state"))
            # Close only this destination's ControlMaster. The live remote server must survive.
            result = ssh.run("printf BEFORE_DISCONNECT")
            assert result.stdout == "BEFORE_DISCONNECT"
            import subprocess
            closed = subprocess.run(["/usr/bin/ssh", "-F", str(ssh.directory / "ssh_config"), "-S", ssh.control,
                                     "-O", "exit", ssh.alias], capture_output=True, text=True, timeout=5)
            assert closed.returncode == 0, closed.stderr
            def reconnected():
                current = next(e for e in test.ctl("connections", "list")["connections"] if e["kind"] == "remote")
                if current["status"] != "connected":
                    return None
                # A new master is the concrete reconnect condition; do not accept stale connected status.
                if not Path(ssh.control).exists():
                    return None
                info = smoke.endpoint_ctl(current["socket_path"], "server", "info")
                assert info["instance_id"] == restored["instance_id"], "reconnect replaced the server"
                return current
            entry = test.wait(reconnected, "real SSH reconnect", timeout=20)
            control = lambda *args: smoke.endpoint_ctl(entry["socket_path"], *args)
            assert smoke.project(control("state")) == before
            test.save("reconnect.json", {"same_instance": True, "layout_preserved": True, "entry": entry})
            print("PASS real SSH disconnect/reconnect with same server and layout", flush=True)
        # A later GUI reuses the stable endpoint and existing workspace, rather than starting a fresh server.
        with WaterGUI("water-real-reuse.", binary=os.environ.get("WATER_UPGRADE_BIN", smoke.binary), variant=variant) as test:
            entry, control = connect(test, ssh.alias)
            reused = control("server", "info")
            ssh.record_remote(reused)
            assert reused["instance_id"] == restored["instance_id"]
            assert smoke.project(control("state")) == before
            snapshot = test.ctl("ui", "snapshot")
            assert snapshot["server_status"]["compatibility"]["compatible"]
            assert not snapshot["server_status"]["compatibility"]["restart_recommended"]
            if os.environ.get("WATER_UPGRADE_BIN"):
                assert snapshot["server_status"]["client"]["version"] != reused["server_version"]
            test.save("reuse.json", {"same_instance": True, "layout_preserved": True, "snapshot": snapshot})
            print("PASS real SSH compatible GUI reuses the existing server", flush=True)
    print("PASS real OpenSSH suite and owned daemon/master/server/key cleanup", flush=True)


if __name__ == "__main__":
    main()
