#!/usr/bin/env python3
"""Measure rendered-frame gaps for paced output and rapid GUI text input."""
import argparse
import json
import os
from pathlib import Path
import shlex
import socket
import struct
import subprocess
import tempfile
import time
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--water", type=Path, required=True)
parser.add_argument("--seconds", type=int, default=8)
args = parser.parse_args()
if not 3 <= args.seconds <= 15:
    parser.error("seconds must be 3..15 per phase")
water = str(args.water.resolve())
directory = Path(tempfile.mkdtemp(prefix="water-cadence.", dir="/tmp"))
control = str(directory / "control.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"server": {"detached": False},
    "terminal": {"font_family": "Go Mono"}, "ui": {"font_family": "Go Mono"},
    "shell": {"program": shell, "args": ["-f"]}}))
with socket.socket() as reservation:
    reservation.bind(("127.0.0.1", 0))
    port = reservation.getsockname()[1]
env = dict(os.environ, WATER_DIAGNOSTIC_PPROF=f"127.0.0.1:{port}")
log = (directory / "gui.log").open("w")
gui = subprocess.Popen([water, "--config", str(config), "--control-socket", control],
                       env=env, stdout=log, stderr=log)


def rpc(method, params=None):
    # The same bounded framed control transport used by water ctl; avoid CLI
    # startup cost obscuring a 20ms keyboard-repeat cadence.
    payload = json.dumps({"build_variant": "dev", "protocol_version": 4,
                          "request_id": 1, "method": method, "params": params or {}}).encode()
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(3)
        conn.connect(control)
        conn.sendall(struct.pack(">I", len(payload)) + payload)
        stream = conn.makefile("rb")
        while True:
            header = stream.read(4)
            if len(header) != 4:
                raise RuntimeError("control connection closed")
            size = struct.unpack(">I", header)[0]
            if size > 16 << 20:
                raise RuntimeError("oversize frame")
            reply = json.loads(stream.read(size))
            if reply.get("request_id") == 1 and "ok" in reply:
                if not reply["ok"]:
                    raise RuntimeError(str(reply.get("error")))
                return reply.get("result")


def timings(reset=False):
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/debug/latency" +
                                ("?reset=1" if reset else ""), timeout=3) as response:
        return json.load(response)


def send(terminal, text):
    ack = rpc("command.dispatch", {"command": {"type": "terminal.send_text",
                  "terminal_id": terminal, "text": text}})
    operation = rpc("operation.wait", {"operation_id": ack["operation_id"]})
    if operation["status"] != "succeeded":
        raise RuntimeError(str(operation))


try:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            state = rpc("ui.snapshot")
            terminal = state.get("frame_active_terminal")
            if terminal and state.get("native_menu", {}).get("ready"):
                break
        except (OSError, RuntimeError):
            pass
        time.sleep(.03)
    else:
        raise RuntimeError("GUI did not become ready")
    python = str(Path.home() / ".venv/bin/python")
    output = directory / "output.py"
    output.write_text("import time,sys\nstart=time.monotonic()\nfor n in range(" + str(args.seconds*50+100) +
                      "):\n time.sleep(max(0,start+n*.02-time.monotonic()))\n print('PACE_%05d'%n,flush=True)\n")
    send(terminal, shlex.join([python, str(output)]) + "\n")
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        state = rpc("ui.snapshot")
        if state.get("active_terminal_last_seq", 0) > 5:
            break
    timings(True)
    # Fixed-duration sampling is the experiment, not a readiness wait.
    time.sleep(args.seconds)
    report = {"output_50_hz": timings()}
    rpc("ui.keystroke", {"keystroke": "ctrl-c"})
    echo = directory / "echo.py"
    echo.write_text("import os,sys,termios,tty\nfd=0\nold=termios.tcgetattr(fd)\ntty.setraw(fd)\n"
                    "try:\n print('ECHO_READY',flush=True)\n while True:\n  b=os.read(fd,1)\n  "
                    "os.write(1,b)\nfinally:\n termios.tcsetattr(fd,termios.TCSANOW,old)\n")
    send(terminal, shlex.join([python, str(echo)]) + "\n")
    subprocess.run([water, "ctl", "--socket", control, "terminal", "contains",
                    "--terminal", terminal, "ECHO_READY", "--timeout-ms", "5000"],
                   check=True, capture_output=True, timeout=6)
    timings(True)
    start = time.monotonic()
    latencies = []
    for n in range(args.seconds*50):
        time.sleep(max(0, start+n*.02-time.monotonic()))
        before = time.monotonic()
        rpc("ui.keystroke", {"keystroke": "text:x"})
        latencies.append((time.monotonic()-before)*1000)
    report["input_50_hz"] = timings()
    ordered = sorted(latencies)
    report["input_roundtrip_ms"] = {"p99": ordered[min(len(ordered)-1,len(ordered)*99//100)],
                                    "max": max(ordered), "samples": len(ordered)}
    report["directory"] = str(directory)
    (directory / "report.json").write_text(json.dumps(report, indent=2))
    print(json.dumps(report))
finally:
    if gui.poll() is None:
        gui.terminate()
        try:
            gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gui.kill()
            gui.wait(timeout=3)
    log.close()
