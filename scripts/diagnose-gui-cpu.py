#!/usr/bin/env python3
"""Measure idle and bounded ANSI output CPU in an owned, isolated native GUI."""
import argparse
import json
import os
from pathlib import Path
import shlex
import socket as network_socket
import subprocess
import tempfile
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--water", type=Path, required=True)
    parser.add_argument("--seconds", type=float, default=5)
    parser.add_argument("--rate-mib", type=float, default=0.25)
    parser.add_argument("--profile", action="store_true", help="requires a water_cpu_diagnostic build")
    args = parser.parse_args()
    if not 1 <= args.seconds <= 15 or not 0 < args.rate_mib <= 4:
        parser.error("seconds must be 1..15 and rate-mib must be 0..4")
    water = str(args.water.resolve())
    root = Path(__file__).resolve().parent.parent
    directory = Path(tempfile.mkdtemp(prefix="water-cpu.", dir="/tmp"))
    socket = str(directory / "control.sock")
    config = directory / "config.json"
    shell = next(str(p) for p in (Path("/opt/homebrew/bin/zsh"), Path("/bin/zsh")) if p.exists())
    config.write_text(json.dumps({"startup": {"window_columns": 96, "window_rows": 30},
                                  "server": {"detached": False},
                                  "shell": {"program": shell, "args": ["-f"]}}))
    env = dict(os.environ, WATER_CONTROL_SOCKET=socket, WATER_CONFIG=str(config))
    if args.profile:
        with network_socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            profile_port = reservation.getsockname()[1]
        env["WATER_DIAGNOSTIC_PPROF"] = f"127.0.0.1:{profile_port}"
    log = (directory / "gui.log").open("w")
    gui = subprocess.Popen([water, "--config", str(config), "--control-socket", socket], env=env, stdout=log, stderr=log)

    def ctl(*command):
        result = subprocess.run([water, "ctl", "--socket", socket, *command], env=env, capture_output=True, text=True, timeout=6)
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        return json.loads(result.stdout)

    def cpu_time():
        value = subprocess.check_output(["ps", "-p", str(gui.pid), "-o", "time="], text=True).strip()
        parts = value.split(":")
        return sum(float(v) * 60 ** i for i, v in enumerate(reversed(parts)))

    def measure(label):
        def allocations(suffix):
            with urllib.request.urlopen(f"http://127.0.0.1:{profile_port}/debug/pprof/allocs", timeout=5) as response:
                (directory / f"{label}-allocs-{suffix}.pprof").write_bytes(response.read())
        if args.profile:
            allocations("before")
        started, cpu = time.monotonic(), cpu_time()
        if args.profile:
            native_sample = subprocess.Popen(["sample", str(gui.pid), str(int(args.seconds)), "5", "-file", str(directory / f"{label}.sample")], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            with urllib.request.urlopen(f"http://127.0.0.1:{profile_port}/debug/pprof/profile?seconds={int(args.seconds)}", timeout=args.seconds + 5) as response:
                (directory / f"{label}.pprof").write_bytes(response.read())
            native_sample.wait(timeout=5)
        deadline = started + args.seconds
        while time.monotonic() < deadline:
            if gui.poll() is not None:
                raise RuntimeError("GUI exited during measurement")
            # This interval defines the measurement window, not readiness.
            time.sleep(min(0.1, max(0, deadline - time.monotonic())))
        elapsed = time.monotonic() - started
        cpu_percent = round((cpu_time() - cpu) / elapsed * 100, 2)
        if args.profile:
            allocations("after")
        return {"mode": label, "cpu_percent": cpu_percent}

    try:
        deadline = time.monotonic() + 10
        while True:
            if gui.poll() is not None or time.monotonic() >= deadline:
                raise RuntimeError(f"GUI failed to become ready; see {directory}")
            try:
                state = ctl("ui", "snapshot")
                terminal = state.get("frame_active_terminal")
                if terminal and terminal != "00000000-0000-0000-0000-000000000000":
                    break
            except (RuntimeError, subprocess.TimeoutExpired):
                pass
        ctl("terminal", "contains", "--terminal", terminal, "%", "--timeout-ms", "5000")
        records = [measure("idle")]
        emitter = root / "scripts/go-ui-background-smoke.py"
        command = (f"{shlex.quote(str(Path.home() / '.venv/bin/python'))} {shlex.quote(str(emitter))} "
                   f"--emit-output ansi --emit-seconds {args.seconds + 2} --rate-mib {args.rate_mib}; printf 'WATER_CPU_%s\\n' DONE\n")
        ctl("terminal", "send", "--terminal", terminal, command)
        ctl("terminal", "contains", "--terminal", terminal, "WATER_OUTPUT_LOAD_START", "--timeout-ms", "5000")
        records.append(measure("ansi-output"))
        ctl("terminal", "contains", "--terminal", terminal, "WATER_CPU_DONE", "--timeout-ms", "5000")
        ctl("ui", "screenshot", "--output", str(directory / "screen.png"))
        report = {"gui_pid": gui.pid, "water": water, "rate_mib": args.rate_mib, "seconds": args.seconds, "records": records}
        (directory / "report.json").write_text(json.dumps(report, indent=2))
        print(json.dumps(dict(report, directory=str(directory))))
    finally:
        # Only the process we spawned is signalled; its server is embedded.
        if gui.poll() is None:
            gui.terminate()
            try:
                gui.wait(timeout=5)
            except subprocess.TimeoutExpired:
                gui.kill()
                gui.wait(timeout=5)
        log.close()


if __name__ == "__main__":
    main()
