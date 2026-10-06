#!/usr/bin/env python3
"""Owned SSH stand-in: real Unix forwarding and server processes, no remote host."""
import hashlib
import json
import os
from pathlib import Path
import selectors
import shlex
import signal
import socket
import subprocess
import sys
import threading
import time

root = Path(os.environ["WATER_SSH_FIXTURE_DIR"])

def proxy(local, remote):
    listener = socket.socket(socket.AF_UNIX)
    listener.bind(local)
    listener.listen()
    def stop(signum, frame):
        listener.close()
        Path(local).unlink(missing_ok=True)
        raise SystemExit(0)
    signal.signal(signal.SIGTERM, stop)
    def connection(client):
        target = socket.socket(socket.AF_UNIX)
        try:
            target.connect(remote)
            with selectors.DefaultSelector() as selected:
                selected.register(client, selectors.EVENT_READ, target)
                selected.register(target, selectors.EVENT_READ, client)
                while True:
                    for key, _ in selected.select():
                        data = key.fileobj.recv(65536)
                        if not data:
                            return
                        key.data.sendall(data)
        except (OSError, ConnectionError):
            pass
        finally:
            client.close()
            target.close()
    while True:
        client, _ = listener.accept()
        threading.Thread(target=connection, args=(client,), daemon=True).start()

if sys.argv[1:2] == ["--proxy"]:
    proxy(sys.argv[2], sys.argv[3])
    sys.exit(0)
args = sys.argv[1:]
if "-O" in args:
    action = args[args.index("-O") + 1]
    if action == "check":
        sys.exit(0)
    flag = "-L" if action == "forward" else "-L"
    local, remote = args[args.index(flag) + 1].split(":", 1)
    record = root / ("forward-" + hashlib.sha256(local.encode()).hexdigest() + ".json")
    if action == "forward":
        with (root / "proxy.log").open("a") as log:
            process = subprocess.Popen([sys.executable, __file__, "--proxy", local, remote], stdout=log, stderr=log, start_new_session=True)
        record.write_text(json.dumps({"pid": process.pid, "local": local, "remote": remote}))
        deadline = time.monotonic() + 2
        while not Path(local).exists():
            if process.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("owned forward failed to start")
            time.sleep(.01)
    elif action == "cancel" and record.exists():
        info = json.loads(record.read_text())
        try:
            os.kill(info["pid"], signal.SIGTERM)
        except ProcessLookupError:
            pass
        record.unlink()
    sys.exit(0)
if "-M" in args:
    sys.exit(0)
script = args[-1]
script = script.replace("/tmp/water-go-dev-server.log", shlex.quote(str(root / "server.log")))
if script.rstrip().endswith("&"):
    script += " water_fixture_pid=$!; printf '%s\\n' \"$water_fixture_pid\" >> " + shlex.quote(str(root / "server-pids.txt"))
result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, timeout=12)
sys.stdout.buffer.write(result.stdout)
sys.stderr.buffer.write(result.stderr)
sys.exit(result.returncode)
