#!/usr/bin/env python3
"""Read-only macOS GUI latency monitor; sample the GUI while a probe is blocked."""

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time


def positive(value):
    number = float(value)
    if not 0 < number < float("inf"):
        raise argparse.ArgumentTypeError("must be a finite positive number")
    return number


def gui_pid(binary, server_pid):
    # Match the executable path, never a process-name substring. No process is killed.
    listing = subprocess.check_output(
        ["ps", "-axo", "pid=,comm="], text=True, timeout=5
    )
    matches = []
    for line in listing.splitlines():
        fields = line.strip().split(None, 1)
        if len(fields) != 2:
            continue
        pid, executable = fields
        if Path(executable).resolve() == binary and int(pid) != server_pid:
            matches.append(int(pid))
    if len(matches) != 1:
        raise RuntimeError(f"expected one running GUI at {binary}; found {matches}")
    return matches[0]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--water", required=True, help="executable of the running GUI")
    parser.add_argument("--socket", help="control socket; otherwise use CLI defaults")
    parser.add_argument("--duration", type=positive, default=60, help="monitor seconds")
    parser.add_argument("--threshold-ms", type=positive, default=500)
    parser.add_argument("--interval", type=positive, default=0.5, help="probe interval seconds")
    args = parser.parse_args()
    if sys.platform != "darwin" or not shutil.which("sample"):
        parser.error("requires macOS with the sample command")
    executable = shutil.which(args.water) or args.water
    binary = Path(executable).expanduser().resolve(strict=True)
    ctl = [str(binary), "ctl"]
    if args.socket:
        ctl += ["--socket", args.socket]
    info = json.loads(subprocess.check_output(ctl + ["server", "info"], timeout=5))
    pid = gui_pid(binary, info["server_pid"])
    output = Path(tempfile.mkdtemp(prefix="water-gui-stalls-"))
    os.chmod(output, 0o700)
    metadata = {"gui_pid": pid, "binary": str(binary), "server": info,
                "threshold_ms": args.threshold_ms}
    (output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(f"Monitoring GUI PID {pid}; switch away and back as usual. Reports: {output}", flush=True)
    deadline = time.monotonic() + args.duration
    count = stalls = 0
    maximum = 0.0
    with (output / "latency.jsonl").open("w") as log:
        while time.monotonic() < deadline:
            started = time.monotonic()
            count += 1
            sampler = None
            # ui state crosses the real GUI event loop; server ping does not.
            with subprocess.Popen(ctl + ["ui", "state"], stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE) as probe:
                try:
                    stdout, _ = probe.communicate(timeout=args.threshold_ms / 1000)
                except subprocess.TimeoutExpired:
                    stalls += 1
                    # Recheck identity before sampling: the original GUI may have exited.
                    if gui_pid(binary, info["server_pid"]) != pid:
                        probe.kill()
                        probe.communicate()
                        raise RuntimeError("GUI process changed; stopped monitoring")
                    trace = output / f"stall-{count:04d}.sample.txt"
                    sampler = subprocess.Popen(
                        ["sample", str(pid), "2", "10", "-file", str(trace)],
                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                    )
                    print(f"Slow GUI probe #{count}; sampling PID {pid}", flush=True)
                    try:
                        stdout, _ = probe.communicate(timeout=5)
                    except subprocess.TimeoutExpired:
                        # Only terminate the monitor's own read-only CLI child.
                        probe.kill()
                        stdout, _ = probe.communicate()
                finally:
                    if probe.poll() is None:
                        probe.kill()
                        probe.communicate()
            elapsed = (time.monotonic() - started) * 1000
            maximum = max(maximum, elapsed)
            record = {"time": time.time(), "latency_ms": round(elapsed, 2),
                      "exit_code": probe.returncode}
            if probe.returncode == 0:
                state = json.loads(stdout)
                record["has_active_window"] = state.get("has_active_window")
            log.write(json.dumps(record) + "\n")
            log.flush()
            if sampler is not None:
                try:
                    sampler.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    sampler.kill()
                    sampler.wait()
                if sampler.returncode != 0:
                    print("Stack sampling failed; latency record retained.", file=sys.stderr)
            if probe.returncode != 0:
                raise RuntimeError(f"GUI probe failed (exit {probe.returncode}); reports: {output}")
            # Rate limiting is outside the app; it is not a test completion condition.
            delay = min(args.interval, max(0, deadline - time.monotonic()))
            time.sleep(delay)
    print(f"{count} probes, {stalls} slow probes, max {maximum:.1f} ms. Reports: {output}")
    if not stalls:
        print("No stall captured in this interval; this does not rule out intermittent hangs.")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, subprocess.SubprocessError, ValueError) as error:
        sys.exit(str(error))
