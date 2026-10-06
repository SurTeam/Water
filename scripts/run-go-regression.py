#!/usr/bin/env python3
"""Run a documented, bounded Water regression profile and retain its evidence."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import uuid

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--profile", choices=("unit", "terminal", "agent", "full"), default="full")
parser.add_argument("--list", action="store_true", help="print the plan without running it")
args = parser.parse_args()
version = (root / "VERSION").read_text().strip()
run_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
directory = root / "target/test-runs" / run_id
binary = directory / "bin/water-test-gui"
server = directory / "bin/water-srv-dev"
server_revision = subprocess.run([sys.executable, str(root / "scripts/server-revision.py")], cwd=root, check=True, capture_output=True, text=True).stdout.strip()
ldflags = "-X github.com/SurTeam/Water/internal/gobuild.Variant=dev -X github.com/SurTeam/Water/internal/gobuild.Version=" + version + " -X github.com/SurTeam/Water/internal/gobuild.ServerRevision=" + server_revision
plan = [("vet", ["go", "vet", "./..."]),
        ("unit", ["go", "test", "./...", "-count=1", "-timeout=60s"]),
        ("race", ["go", "test", "-race", "./internal/goclient", "./internal/goserver", "./internal/goterminal", "./internal/govt", "./internal/xterm", "./internal/goui", "-timeout=60s"])]
if args.profile != "unit":
    plan += [("build-gui", ["go", "build", "-ldflags", ldflags, "-o", str(binary), "./cmd/water"]),
             ("build-server", ["go", "build", "-ldflags", ldflags, "-o", str(server), "./cmd/water-server"]),
             ("scenarios", ["bash", "scripts/run-go-scenario-suite.sh"]),
             ("gui", ["bash", "scripts/run-go-ui-smoke.sh"])]
    scripts = ["go-ui-content-smoke.py", "go-ui-server-smoke.py"]
    if args.profile in ("terminal", "full"):
        scripts += ["go-ui-query-smoke.py", "go-ui-graphics-history-smoke.py", "go-ui-pi-redraw-smoke.py"]
    if args.profile in ("agent", "full"):
        scripts += ["go-ui-progress-smoke.py", "go-ui-agent-smoke.py"]
        if args.profile == "agent":
            scripts += ["go-ui-pi-redraw-smoke.py"]
    plan += [(name.removesuffix(".py"), [sys.executable, "scripts/" + name]) for name in scripts]
if args.list:
    print(json.dumps({"profile": args.profile, "version": version, "step_timeout_seconds": 60,
                      "steps": [{"name": name, "command": command} for name, command in plan]}, indent=2))
    sys.exit(0)

directory.mkdir(parents=True)
env = dict(os.environ, WATER_BIN=str(binary), WATER_SERVER_BIN=str(server), WATER_SCENARIO_REQUIRE_ALL="1",
           WATER_KEEP_UI_SMOKE="1", PYTHONDONTWRITEBYTECODE="1", WATER_TEST_INSTANCE="regression-" + run_id,
           WATER_TEST_EVIDENCE_DIR=str(directory / "gui-artifacts"))
if args.profile in ("terminal", "full"):
    from PIL import Image
    image = directory / "fixture.png"
    Image.new("RGB", (320, 180), (72, 130, 180)).save(image)
    env["WATER_TEST_IMAGE"] = str(image)
report = {"profile": args.profile, "version": version, "steps": [], "passed": False}
report_path = directory / "summary.json"

for name, command in plan:
    print("RUN " + name, flush=True)
    started = time.monotonic()
    log_path = directory / (name + ".log")
    timed_out = False
    with log_path.open("w") as log:
        process = subprocess.Popen(command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT,
                                   start_new_session=True)
        try:
            code = process.wait(timeout=60)
        except (subprocess.TimeoutExpired, KeyboardInterrupt) as error:
            timed_out = isinstance(error, subprocess.TimeoutExpired)
            # The group was created by this exact test step, never by process name.
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
            code = 124 if timed_out else 130
    report["steps"].append({"name": name, "command": command, "pid": process.pid,
                            "elapsed_seconds": round(time.monotonic()-started, 3),
                            "exit_code": code, "timed_out": timed_out, "log": str(log_path)})
    report_path.write_text(json.dumps(report, indent=2))
    output = log_path.read_text()
    if code:
        print("FAIL " + name + "\n" + "\n".join(output.splitlines()[-50:]), flush=True)
        print("artifacts=" + str(directory), flush=True)
        sys.exit(code)
    useful = [line for line in output.splitlines() if line.startswith("PASS ") or "artifacts=" in line or "retained at" in line or "scenario " in line]
    print("PASS " + name + " (" + str(report["steps"][-1]["elapsed_seconds"]) + "s)", flush=True)
    for line in useful:
        print(line, flush=True)
report["passed"] = True
report_path.write_text(json.dumps(report, indent=2))
print("PASS profile=" + args.profile + " version=" + version + "; artifacts=" + str(directory), flush=True)
