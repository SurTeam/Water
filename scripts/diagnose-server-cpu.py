#!/usr/bin/env python3
"""Replay a private local recording against an owned standalone dev server.

Recording: agent-N.json terminal.snapshot responses and live-N.json lists of
{at: seconds, data: base64} output events. Never commit terminal recordings.
CPU is process CPU seconds / wall seconds, with one core equal to 100%.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time


def receive(sock, length):
    parts = []
    while length:
        part = sock.recv(length)
        if not part:
            raise EOFError("control socket closed")
        parts.append(part)
        length -= len(part)
    return b"".join(parts)


def frame(sock):
    length = struct.unpack(">I", receive(sock, 4))[0]
    if length > 16 * 1024 * 1024:
        raise ValueError("oversized frame")
    return receive(sock, length)


def send(sock, method, params):
    payload = json.dumps(dict(build_variant="dev", protocol_version=5,
                              request_id=1, method=method, params=params)).encode()
    sock.sendall(struct.pack(">I", len(payload)) + payload)


def rpc(path, method, params=None):
    with socket.socket(socket.AF_UNIX) as sock:
        sock.settimeout(15)
        sock.connect(str(path))
        send(sock, method, params or {})
        result = json.loads(frame(sock))
        if result.get("error"):
            raise RuntimeError(result["error"])
        return result["result"]


def dispatch(path, command):
    result = rpc(path, "command.dispatch", {"command": command})
    operation = rpc(path, "operation.get", result)
    if operation["status"] != "succeeded":
        raise RuntimeError(operation)
    return operation["result"]


def wait_for(condition, label, seconds=55):
    deadline = time.monotonic() + seconds
    while not condition():
        if time.monotonic() >= deadline:
            raise TimeoutError(label)
        time.sleep(0.01)  # Poll an observable condition, not a readiness delay.


def cpu_seconds(pid):
    value = subprocess.check_output(["ps", "-p", str(pid), "-o", "time="], text=True).strip()
    total = 0.0
    for part in value.split(":"):
        total = total * 60 + float(part)
    return total


def recording(root, index):
    snapshot = json.loads((root / f"agent-{index}.json").read_text())
    seed = b"".join(base64.b64decode(e.get("bytes", ""))
                    for e in snapshot["events"] if e["type"] == "output")
    events = [(e["at"], base64.b64decode(e["data"]))
              for e in json.loads((root / f"live-{index}.json").read_text())]
    return snapshot["size"], seed, events


def write_all(data):
    view = memoryview(data)
    while view:
        view = view[os.write(1, view):]


def writer(args):
    attributes = termios.tcgetattr(1)
    attributes[1] &= ~termios.OPOST  # Captured PTY bytes already contain CR/LF.
    termios.tcsetattr(1, termios.TCSANOW, attributes)
    root, run = Path(args.recording), Path(args.run)
    _, seed, events = recording(root, args.writer)
    wait_for(lambda: (run / "seed-go").exists(), "seed trigger")
    write_all(seed)
    wait_for(lambda: (run / "start.json").exists(), "load trigger")
    start = json.loads((run / "start.json").read_text())["start"]
    for cycle in range(args.cycles):
        for at, data in events:
            remaining = start + cycle * args.seconds + at - time.monotonic()
            if remaining > 0:
                threading.Event().wait(remaining)
            write_all(data)
    remaining = start + args.seconds * args.cycles - time.monotonic()
    if remaining > 0:
        threading.Event().wait(remaining)
    write_all(f"\r\nWATER_LOAD_DONE_{args.writer}\r\n".encode())
    # Keep PTY alive until the owned server is shut down.
    threading.Event().wait(50)


def pane_for_tab(state, tab_id):
    def leaf(tree):
        if "pane_id" in tree:
            return tree["pane_id"]
        return leaf(tree["first"])
    for workspace in state["workspaces"]:
        for tab in workspace["tabs"]:
            if tab["id"] == tab_id:
                return leaf(tab["tree"])
    raise ValueError("created tab missing")


def run(args):
    root = Path(args.recording).resolve()
    run_dir = Path(tempfile.mkdtemp(prefix="water-server-cpu.", dir="/tmp"))
    path = run_dir / "control.sock"
    cfg = run_dir / "config.json"
    zsh = shutil.which("zsh")
    if not zsh:
        raise RuntimeError("zsh required")
    cfg.write_text(json.dumps({"terminal": {"scrollback_lines": 10000},
                               "shell": {"program": zsh, "args": ["-f"]}}))
    datasets = [recording(root, i) for i in range(6)]
    if args.seconds <= 0 or args.cycles < 1:
        raise ValueError("seconds and cycles must be positive")
    if any(at > args.seconds for _, _, events in datasets for at, _ in events):
        raise ValueError("seconds is shorter than the recording")
    expected = []
    for i, (_, seed, events) in enumerate(datasets):
        data = seed + b"".join(data for _, data in events) * args.cycles + f"\r\nWATER_LOAD_DONE_{i}\r\n".encode()
        expected.append((len(data), hashlib.sha256(data).hexdigest()))
    sockets, threads, errors, terminal_ids = [], [], [], []
    counts = [0] * 6
    hashes = [hashlib.sha256() for _ in range(6)]
    stop = threading.Event()
    log = (run_dir / "server.log").open("w")
    server = subprocess.Popen([str(Path(args.server).resolve()), "--control-socket", str(path),
                               "--config", str(cfg), "--empty-workspace"], stdout=log, stderr=log)
    (run_dir / "ownership.json").write_text(json.dumps({"server_pid": server.pid, "socket": str(path)}))
    print(f"evidence: {run_dir}", flush=True)
    try:
        def ready():
            if server.poll() is not None:
                raise RuntimeError("server exited: " + (run_dir / "server.log").read_text())
            if not path.exists():
                return False
            return bool(rpc(path, "server.info"))
        wait_for(ready, "server readiness")
        workspaces = [dispatch(path, {"type": "workspace.create"})["workspace_id"] for _ in range(7)]
        def consume(index, sock):
            try:
                while not stop.is_set():
                    try:
                        payload = frame(sock)
                    except socket.timeout:
                        continue
                    if payload[:4] == b"\0WT4" and payload[4] == 1:
                        data = payload[33:]
                        counts[index] += len(data)
                        hashes[index].update(data)
            except (OSError, EOFError) as exc:
                if not stop.is_set():
                    errors.append(str(exc))
        for i in range(29):
            tab = dispatch(path, {"type": "tab.new", "workspace_id": workspaces[i % 7]})["tab_id"]
            if i >= 6:
                continue
            pane = pane_for_tab(rpc(path, "state.dump"), tab)
            size = datasets[i][0]
            result = dispatch(path, {"type": "terminal.spawn", "pane_id": pane,
                                    "program": sys.executable,
                                    "args": [str(Path(__file__).resolve()), "--writer", str(i),
                                             "--recording", str(root), "--run", str(run_dir),
                                             "--seconds", str(args.seconds),
                                             "--cycles", str(args.cycles)], **size})
            terminal_ids.append(result["terminal_id"])
            sock = socket.socket(socket.AF_UNIX)
            sock.settimeout(1)
            sock.connect(str(path))
            send(sock, "terminal.attach", {"terminal_id": result["terminal_id"]})
            response = json.loads(frame(sock))
            if not response.get("ok"):
                raise RuntimeError(response)
            sockets.append(sock)
            thread = threading.Thread(target=consume, args=(i, sock), daemon=True)
            thread.start()
            threads.append(thread)
        (run_dir / "seed-go").touch()
        wait_for(lambda: all(counts[i] == len(datasets[i][1]) for i in range(6)), "seed consumed")
        before = rpc(path, "debug.metrics")
        c0, t0 = cpu_seconds(server.pid), time.monotonic()
        (run_dir / "start.json").write_text(json.dumps({"start": t0}))
        windows = []
        previous_time, previous_cpu = t0, c0
        def completed():
            nonlocal previous_time, previous_cpu
            now = time.monotonic()
            if now - previous_time >= 5:
                current_cpu = cpu_seconds(server.pid)
                windows.append({"seconds": now - previous_time,
                                "cpu_percent": 100 * (current_cpu - previous_cpu) / (now - previous_time)})
                previous_time, previous_cpu = now, current_cpu
            if errors:
                raise RuntimeError(errors)
            return all(counts[i] == expected[i][0] for i in range(6))
        wait_for(completed, "all output consumed", args.seconds * args.cycles + 15)
        elapsed, cpu = time.monotonic() - t0, cpu_seconds(server.pid) - c0
        after = rpc(path, "debug.metrics")
        if errors:
            raise RuntimeError(errors)
        for i in range(6):
            assert hashes[i].hexdigest() == expected[i][1], f"stream {i} differs"
        replay_hashes = []
        for terminal_id in terminal_ids:
            snapshot = rpc(path, "terminal.snapshot", {"terminal_id": terminal_id})
            data = b"".join(base64.b64decode(e.get("bytes", ""))
                            for e in snapshot["events"] if e["type"] == "output")
            replay_hashes.append({"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
        report = {"server": str(Path(args.server).resolve()), "server_pid": server.pid,
                  "seconds": elapsed, "cpu_seconds": cpu, "cpu_percent": 100 * cpu / elapsed,
                  "terminals": 29, "workspaces": 7,
                  "seed_bytes": sum(len(seed) for _, seed, _ in datasets),
                  "recorded_events": sum(len(events) for _, _, events in datasets) * args.cycles,
                  "recorded_bytes": sum(len(data) for _, _, events in datasets for _, data in events) * args.cycles,
                  "cpu_windows": windows, "final_replay": replay_hashes,
                  "stream_sha256_verified": True,
                  "metrics_delta": {k: after[k] - before[k] for k in before
                                    if isinstance(before[k], (int, float))}}
        (run_dir / "report.json").write_text(json.dumps(report, indent=2))
        print(json.dumps(report, indent=2), flush=True)
    finally:
        stop.set()
        for sock in sockets:
            sock.close()
        server.terminate()
        try:
            server.wait(timeout=10)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait(timeout=5)
        for thread in threads:
            thread.join(timeout=2)
        log.close()
        (run_dir / "cleanup.json").write_text(json.dumps({"server_pid": server.pid,
                                                         "exit_code": server.returncode,
                                                         "socket_removed": not path.exists()}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server")
    parser.add_argument("--recording", required=True)
    parser.add_argument("--seconds", type=float, default=12)
    parser.add_argument("--cycles", type=int, default=1)
    parser.add_argument("--writer", type=int, help=argparse.SUPPRESS)
    parser.add_argument("--run", help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.writer is not None:
        writer(args)
    else:
        if not args.server:
            parser.error("--server required")
        run(args)


if __name__ == "__main__":
    main()
