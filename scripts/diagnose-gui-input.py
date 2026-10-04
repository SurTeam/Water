#!/usr/bin/env python3
"""Measure real GUI key handling while continuous output fills history."""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--water", type=Path, required=True)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--seconds", type=int, default=25)
    parser.add_argument("--rate-mib", type=float, default=1)
    parser.add_argument("--workload", choices=("plain", "ansi", "unicode", "hyperlinks"), default="ansi")
    parser.add_argument("--trace", action="store_true", help="requires diagnostic build")
    parser.add_argument("--timings", action="store_true", help="sample bounded native frame timings; requires diagnostic build")
    args = parser.parse_args()
    if not 5 <= args.seconds <= 40 or not 0 < args.rate_mib <= 16:
        parser.error("seconds must be 5..40 and rate-mib 0..16")
    directory = Path(tempfile.mkdtemp(prefix="water-input.", dir="/tmp"))
    water = str(args.water.resolve())
    control = str(directory / "control.sock")
    config = directory / "config.json"
    preferences = json.loads(args.config.read_text()) if args.config else {"startup": {"window_columns": 96, "window_rows": 30}}
    preferences.setdefault("server", {})["detached"] = False
    shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
    preferences["shell"] = {"program": shell, "args": ["-f"]}
    config.write_text(json.dumps(preferences))
    env = dict(os.environ, WATER_CONFIG=str(config), WATER_CONTROL_SOCKET=control)
    if args.trace or args.timings:
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        env["WATER_DIAGNOSTIC_PPROF"] = f"127.0.0.1:{port}"
    log = (directory / "gui.log").open("w")
    gui = subprocess.Popen([water, "--config", str(config), "--control-socket", control], env=env, stdout=log, stderr=log)

    def ctl(*command):
        result = subprocess.run([water, "ctl", "--socket", control, *command], env=env, capture_output=True, text=True, timeout=6)
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        return json.loads(result.stdout)

    def trace():
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/debug/pprof/trace?seconds={args.seconds}", timeout=args.seconds+5) as response:
            (directory / "runtime.trace").write_bytes(response.read())

    try:
        deadline = time.monotonic()+10
        while time.monotonic() < deadline:
            if gui.poll() is not None:
                raise RuntimeError(f"GUI exited: {directory}")
            try:
                state = ctl("ui", "snapshot")
                terminal = state.get("frame_active_terminal")
                if terminal and terminal != "00000000-0000-0000-0000-000000000000":
                    break
            except RuntimeError:
                pass
        else:
            raise RuntimeError("GUI readiness timed out")
        ctl("terminal", "contains", "--terminal", terminal, "%", "--timeout-ms", "5000")
        if args.trace or args.timings:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/debug/latency?reset=1",timeout=5) as response:
                response.read()
        emitter = Path(__file__).resolve().parent / "go-ui-background-smoke.py"
        command = shlex.join([str(Path.home()/".venv/bin/python"), str(emitter), "--emit-output", args.workload, "--emit-seconds", str(args.seconds+2), "--rate-mib", str(args.rate_mib)])
        records = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            capture = pool.submit(trace) if args.trace else None
            ctl("terminal", "send", "--terminal", terminal, command+"\n")
            started = time.monotonic()
            deadline = started+args.seconds
            while time.monotonic() < deadline:
                before = time.monotonic()
                result = ctl("ui", "key", "x")
                records.append({"at_seconds": round(before-started,3), "elapsed_ms": round((time.monotonic()-before)*1000,3)})
                if not result.get("handled", True):
                    raise RuntimeError(f"GUI did not handle key: {result}")
                # Sampling cadence defines the experiment, not readiness.
                time.sleep(max(0, .1-(time.monotonic()-before)))
            if capture:
                capture.result(timeout=5)

        native_timings = None
        if args.trace or args.timings:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/debug/latency",timeout=5) as response:
                native_timings = json.load(response)
        ctl("terminal", "send-bytes", "--terminal", terminal, "--hex", "03")
        ctl("ui", "key", "ctrl-u")
        ctl("terminal", "send", "--terminal", terminal, "printf 'WATER_INPUT_%s\\n' OK\n")
        ctl("terminal", "contains", "--terminal", terminal, "WATER_INPUT_OK", "--timeout-ms", "5000")
        ordered = sorted(r["elapsed_ms"] for r in records)
        report = {"directory": str(directory), "gui_pid": gui.pid, "seconds": args.seconds, "rate_mib": args.rate_mib,
                  "workload": args.workload, "samples": len(records), "p50_ms": ordered[len(ordered)//2],
                  "p99_ms": ordered[min(len(ordered)-1,int(len(ordered)*.99))], "max_ms": max(ordered), "records": records}
        if native_timings is not None:
            report["native_timings"] = native_timings
        (directory / "report.json").write_text(json.dumps(report, indent=2))
        print(json.dumps({k:v for k,v in report.items() if k != "records"}))
    finally:
        if gui.poll() is None:
            gui.terminate()
            try:
                gui.wait(timeout=5)
            except subprocess.TimeoutExpired:
                gui.kill(); gui.wait(timeout=5)
        log.close()


if __name__ == "__main__":
    main()
