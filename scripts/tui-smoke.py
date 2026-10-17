#!/usr/bin/env python3
"""Verify the TUI in an isolated real PTY and preserve ownership/cleanup evidence."""

import argparse
import fcntl
import json
import os
import pty
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--binary", required=True)
args = parser.parse_args()
binary = os.path.abspath(args.binary)
root = tempfile.mkdtemp(prefix="water-tui-pty-", dir="/tmp")
socket = root + "/server.sock"
config = root + "/config.json"
with open(config, "w") as stream:
    json.dump({"shell": {"program": "/bin/sh", "args": ["-i"]}}, stream)
env = dict(os.environ, TERM="xterm-256color", WATER_CONFIG=config,
           WATER_CONTROL_SOCKET=socket)
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 28, 120, 0, 0))


def controlling_terminal():
    os.setsid()
    fcntl.ioctl(slave, termios.TIOCSCTTY, 0)


process = subprocess.Popen(
    [binary, "tui", "--socket", socket, "--config", config],
    stdin=slave, stdout=slave, stderr=slave, env=env,
    preexec_fn=controlling_terminal,
)
output = bytearray()
server_pid = None
frontend_pids = [process.pid]
workspace_evidence = []


def drain():
    ready, _, _ = select.select([master], [], [], 0.03)
    if ready:
        try:
            output.extend(os.read(master, 65536))
        except OSError:
            pass  # The PTY may close immediately after terminal restoration.


def until(predicate, seconds=8, allow_exit=False):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        drain()
        if predicate():
            return
        if process.poll() is not None and not allow_exit:
            raise AssertionError(f"frontend exited: {process.returncode}")
    raise AssertionError("timeout: " + output.decode(errors="replace")[-3000:])


def control(*arguments):
    return subprocess.run(
        [binary, "ctl", *arguments], env=env, capture_output=True,
        text=True, timeout=4,
    )


def state():
    result = control("state")
    if result.returncode != 0:
        return None
    current = json.loads(result.stdout)
    current["workspaces"] = current.get("workspaces") or []
    return current


try:
    until(lambda: b"Ctrl-B" in output or b"Commands" in output)
    info = control("server", "info")
    info.check_returncode()
    server_pid = json.loads(info.stdout)["server_pid"]
    os.write(master, b"printf 'TUI_VERIFIED_%s\\n' OUTPUT\n")
    until(lambda: b"TUI_VERIFIED_OUTPUT" in output)
    os.write(master, b"\x02%")

    def has_split():
        current = state()
        return (current and current["workspaces"][0]["tabs"][0]
                ["tree"]["type"] == "split")

    until(has_split)
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 20, 60, 0, 0))
    os.kill(process.pid, signal.SIGWINCH)
    os.write(master, b"\x02p")
    until(lambda: b"Enter: select" in output)
    os.write(master, b"\r\x02q")
    until(lambda: process.poll() is not None, seconds=5, allow_exit=True)
    assert process.returncode == 0, process.returncode
    assert b"\x1b[?1049l" in output, "alternate screen was not restored"
    assert b"\x1b[?1006l" in output, "mouse reporting was not disabled"
    assert has_split(), "detach lost server state"
    # Reattach to the same server and exercise the destructive exit separately.
    with open(root + "/terminal-first-session.bin", "wb") as stream:
        stream.write(output)
    output.clear()
    os.close(master)
    os.close(slave)
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 20, 60, 0, 0))
    process = subprocess.Popen(
        [binary, "tui", "--socket", socket, "--config", config],
        stdin=slave, stdout=slave, stderr=slave, env=env,
        preexec_fn=controlling_terminal,
    )
    frontend_pids.append(process.pid)
    until(lambda: b"Ctrl-B" in output or b"Commands" in output)

    def record_workspace(label):
        current = state()
        assert current is not None, label + ": state unavailable"
        workspace_evidence.append({"label": label, "state": current,
                                   "terminal_bytes": len(output)})
        with open(root + "/workspace-close.json", "w") as stream:
            json.dump(workspace_evidence, stream, indent=2)
        return current

    def workspace_count(count):
        current = state()
        return current and len(current["workspaces"]) == count

    def close_prompt(keys):
        start = len(output)
        os.write(master, keys)
        until(lambda: b"Close workspace? yes>" in output[start:])

    original = record_workspace("before-workspace-close")
    original_id = original["workspaces"][0]["id"]
    close_prompt(b"\x02W")
    assert state()["workspaces"][0]["id"] == original_id, "closed before confirmation"
    # A non-yes response dismisses the prompt without escape-sequence timing
    # ambiguity in the outer terminal's decoder.
    os.write(master, b"no\r")
    assert state()["workspaces"][0]["id"] == original_id, "cancel closed workspace"
    record_workspace("cancel-preserved-workspace")
    close_prompt(b"\x02W")
    os.write(master, b"yes\r")
    until(lambda: workspace_count(0))
    record_workspace("confirmed-workspace-closed")
    assert process.poll() is None, "last workspace close exited TUI"

    # A new workspace contains a real shell tab. Closing just that tab leaves
    # an empty workspace, which W must close without a second confirmation.
    os.write(master, b"\x02w")
    until(lambda: workspace_count(1))
    start = len(output)
    os.write(master, b"\x02t")
    until(lambda: b"Close tab? yes>" in output[start:])
    os.write(master, b"yes\r")
    until(lambda: (lambda current: current and len(current["workspaces"]) == 1
                  and not current["workspaces"][0]["tabs"])(state()))
    record_workspace("empty-workspace-before-close")
    os.write(master, b"\x02W")
    until(lambda: workspace_count(0))
    record_workspace("empty-workspace-closed-without-confirmation")

    # xterm encodes Ctrl-Alt-W as Escape followed by Ctrl-W. Exercise that
    # real tcell decoder path using the shared default binding.
    os.write(master, b"\x02w")
    until(lambda: workspace_count(1))
    close_prompt(b"\x1b\x17")
    assert workspace_count(1), "shortcut closed before confirmation"
    os.write(master, b"yes\r")
    until(lambda: workspace_count(0))
    record_workspace("ctrl-alt-w-confirmed-workspace-close")
    assert control("ping").returncode == 0, "workspace close stopped server"
    os.write(master, b"\x02Q")
    until(lambda: b"Stop server" in output)
    assert control("ping").returncode == 0, "shutdown occurred before confirmation"
    os.write(master, b"yes\r")
    until(lambda: process.poll() is not None, seconds=5, allow_exit=True)
    assert process.returncode == 0, process.returncode
    until(lambda: not os.path.exists(socket), seconds=5, allow_exit=True)
    assert control("ping").returncode != 0, "confirmed shutdown left server running"
    assert b"\x1b[?1049l" in output, "shutdown did not restore alternate screen"
    assert b"\x1b[?1006l" in output, "shutdown did not restore mouse reporting"
    result = {
        "result": "PASS", "evidence": root,
        "frontend_pid": process.pid, "server_pid": server_pid,
        "checks": ["auto-start", "live shell output", "server split",
                   "narrow selector", "detach persistence",
                   "workspace close cancellation and confirmation",
                   "empty workspace closes without confirmation",
                   "Ctrl-Alt-W through real terminal decoder",
                   "alternate-screen and mouse restoration",
                   "confirmed server shutdown and clean TUI exit"],
    }
    with open(root + "/result.json", "w") as stream:
        json.dump(result, stream, indent=2)
    print(json.dumps(result))
finally:
    if process.poll() is None:
        process.terminate()
        deadline = time.monotonic() + 3
        while process.poll() is None and time.monotonic() < deadline:
            drain()
        if process.poll() is None:
            process.kill()
            process.wait(timeout=2)
    with open(root + "/terminal-output.bin", "wb") as stream:
        stream.write(output)
    with open(root + "/ownership.json", "w") as stream:
        json.dump({"frontend_pid": process.pid, "frontend_pids": frontend_pids,
                   "server_pid": server_pid,
                   "socket": socket, "config": config}, stream)
    try:
        cleanup = control("server", "shutdown")
        with open(root + "/cleanup.json", "w") as stream:
            json.dump({"frontend_exit": process.returncode,
                       "shutdown_exit": cleanup.returncode,
                       "shutdown_stderr": cleanup.stderr}, stream)
    finally:
        os.close(master)
        os.close(slave)
