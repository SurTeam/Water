#!/usr/bin/env python3
"""Measure idle and bounded ANSI output CPU in an owned, isolated native GUI."""
import argparse
import json
import os
import re
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
    parser.add_argument("--memory", action="store_true", help="sample diagnostic memory without forced GC or CPU profiling")
    parser.add_argument("--config", type=Path, help="use these preferences in the isolated test instance")
    parser.add_argument("--sustained-seconds", type=int, default=0, help="one continuous output run, sampled repeatedly (0 or 10..45 seconds)")
    parser.add_argument("--cycles", type=int, default=1, help="bounded output cycles for retained-memory comparison (1..3)")
    args = parser.parse_args()
    if not 1 <= args.seconds <= 15 or not 0 < args.rate_mib <= 4 or not 1 <= args.cycles <= 3:
        parser.error("seconds must be 1..15, rate-mib 0..4, and cycles 1..3")
    if args.sustained_seconds and not 10 <= args.sustained_seconds <= 45:
        parser.error("sustained-seconds must be 0 or 10..45")
    water = str(args.water.resolve())
    root = Path(__file__).resolve().parent.parent
    directory = Path(tempfile.mkdtemp(prefix="water-cpu.", dir="/tmp"))
    socket = str(directory / "control.sock")
    config = directory / "config.json"
    shell = next(str(p) for p in (Path("/opt/homebrew/bin/zsh"), Path("/bin/zsh")) if p.exists())
    preferences = json.loads(args.config.read_text()) if args.config else {"startup": {"window_columns": 96, "window_rows": 30}}
    preferences.setdefault("server", {})["detached"] = False
    preferences["shell"] = {"program": shell, "args": ["-f"]}
    config.write_text(json.dumps(preferences))
    env = dict(os.environ, WATER_CONTROL_SOCKET=socket, WATER_CONFIG=str(config))
    if args.profile or args.memory:
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
        rss = int(subprocess.check_output(["ps", "-p", str(gui.pid), "-o", "rss="], text=True)) * 1024
        record = {"mode": label, "cpu_percent": cpu_percent, "rss_bytes": rss}
        summary = subprocess.check_output(["vmmap", "-summary", str(gui.pid)], text=True, stderr=subprocess.STDOUT)
        (directory / f"{label}-vmmap.txt").write_text(summary)
        footprint = re.search(r"Physical footprint:\s+([\d.]+)([KMG])", summary)
        if footprint:
            record["physical_footprint_bytes"] = int(float(footprint[1]) * 1024 ** ("KMG".index(footprint[2]) + 1))
        if args.profile or args.memory:
            with urllib.request.urlopen(f"http://127.0.0.1:{profile_port}/debug/memory", timeout=5) as response:
                record.update(json.load(response))
        return record

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
        for cycle in range(1 if args.sustained_seconds else args.cycles):
            command = (f"printf 'WATER_CPU_%s_%s\\n' START {cycle}; "
                       f"{shlex.quote(str(Path.home() / '.venv/bin/python'))} {shlex.quote(str(emitter))} "
                       f"--emit-output ansi --emit-seconds {args.sustained_seconds or args.seconds + 2} --rate-mib {args.rate_mib}; printf 'WATER_CPU_%s_%s\\n' DONE {cycle}\n")
            ctl("terminal", "send", "--terminal", terminal, command)
            ctl("terminal", "contains", "--terminal", terminal, f"WATER_CPU_START_{cycle}", "--timeout-ms", "5000")
            label = "ansi-output" if args.cycles == 1 else f"ansi-output-{cycle + 1}"
            if args.sustained_seconds:
                deadline = time.monotonic() + args.sustained_seconds
                sample = 0
                while time.monotonic() + args.seconds + 1 < deadline:
                    sample += 1
                    records.append(measure(f"continuous-output-{sample}"))
            else:
                records.append(measure(label))
            ctl("terminal", "contains", "--terminal", terminal, f"WATER_CPU_DONE_{cycle}", "--timeout-ms", "5000")
        if args.sustained_seconds:
            records.append(measure("output-finished"))
        ctl("ui", "screenshot", "--output", str(directory / "screen.png"))
        report = {"gui_pid": gui.pid, "water": water, "rate_mib": args.rate_mib, "seconds": args.seconds, "sustained_seconds": args.sustained_seconds, "forced_gc": args.profile, "records": records}
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
