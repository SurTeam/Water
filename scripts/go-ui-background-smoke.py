#!/usr/bin/env python3
"""Exercise macOS application switching while an isolated Go terminal produces output."""

from water_test import hold_gui_lock, water_processes

import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import uuid

os.environ["WATER_TEST_UNFOCUSED"] = "1"


class OwnershipError(RuntimeError):
    """A verified ownership boundary prevents server/window shutdown."""


def cleanup_background(directory, socket, gui, server_pid, server_instance, existing_water, ctl, owned_server):
    errors = []
    refused = False
    try:
        if Path(socket).exists():
            info = ctl("server", "info")
            owned_pid = owned_server(info)
            if server_instance is not None and info.get("instance_id") != server_instance:
                raise OwnershipError("background server instance changed; refusing shutdown")
            server_pid = owned_pid
            ctl("server", "shutdown")
    except OwnershipError as error:
        refused = True
        errors.append(str(error))
    except Exception as error:
        errors.append(str(error))
    finally:
        # Transport/JSON failures do not revoke ownership of this exact Popen.
        if not refused and gui is not None and gui.poll() is None:
            gui.terminate()
            try:
                gui.wait(timeout=5)
            except subprocess.TimeoutExpired:
                gui.kill()
                gui.wait(timeout=5)
    deadline = time.monotonic() + 5
    current = water_processes()
    while server_pid in current and time.monotonic() < deadline:
        time.sleep(.025)
        current = water_processes()
    cleanup = {"cleanup_refused": refused, "gui_exited": gui is None or gui.poll() is not None,
               "server_exited": server_pid not in current, "socket_removed": not Path(socket).exists(),
               "existing_water_preserved": all(current.get(pid) == command for pid, command in existing_water.items()),
               "errors": errors}
    passed = not errors and all(cleanup[key] for key in
        ("gui_exited", "server_exited", "socket_removed", "existing_water_preserved"))
    (directory / "cleanup.json").write_text(json.dumps(cleanup, indent=2))
    print(f"{'PASS' if passed else 'FAIL'} owned background cleanup; artifacts: {directory}", flush=True)
    if not passed:
        raise RuntimeError("Background cleanup failed; inspect " + str(directory / "cleanup.json"))


def finish_background(directory, records, samplers, cleanup, primary_error=None):
    errors = []
    try:
        try:
            (directory / "latency.json").write_text(json.dumps(records, indent=2) + "\n")
        except Exception as error:
            errors.append(error)
        finally:
            for sampler in samplers:
                try:
                    sampler.wait(timeout=5)
                except Exception as error:
                    # Attempt every sampler even if an earlier sampler failed.
                    try:
                        if sampler.poll() is None:
                            sampler.kill()
                            sampler.wait(timeout=5)
                    except Exception as stop_error:
                        errors.append(stop_error)
                    if not isinstance(error, subprocess.TimeoutExpired):
                        errors.append(error)
    finally:
        try:
            cleanup()
        except Exception as error:
            errors.append(error)
    if errors:
        message = "Background cleanup/evidence failed: " + "; ".join(map(str, errors))
        if primary_error is None:
            raise RuntimeError(message) from errors[0]
        if hasattr(primary_error, "add_note"):
            primary_error.add_note(message)
        print("FAIL " + message, file=sys.stderr, flush=True)


def wait_snapshot(ctl, gui, predicate, description, *, transient=False, measured=False, target_socket=None):
    deadline = time.monotonic() + 10
    state = {}
    while time.monotonic() < deadline:
        if gui.poll() is not None:
            raise RuntimeError(f"GUI exited waiting for {description}")
        try:
            state = ctl("ui", "snapshot", target_socket=target_socket, measured=measured)
        except (RuntimeError, ValueError, subprocess.SubprocessError):
            if not transient:
                raise
        else:
            if predicate(state):
                return state
        time.sleep(.025)
    diagnostics = {key: state.get(key) for key in
        ("window_position", "window_size", "window_focused", "window_occluded", "application_hidden", "window_minimized")}
    diagnostics["native"] = {key: state.get("native_menu", {}).get(key) for key in ("window_opaque", "occluded", "hidden")}
    print(f"Last window state: {json.dumps(diagnostics)}", flush=True)
    raise RuntimeError(f"Timeout waiting for {description}")


def resolve_server(source):
    """Inspect descriptors without starting a GUI or server listener."""
    candidates = ([Path(os.environ["WATER_SERVER_BIN"]).resolve()] if os.environ.get("WATER_SERVER_BIN")
                  else [source.parent / "water-srv-dev", source.parent / "water-server"])
    try:
        client = subprocess.run([str(source), "ctl", "client", "info"], capture_output=True,
                                text=True, timeout=5, check=True)
        client_info = json.loads(client.stdout)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        raise RuntimeError("Background smoke cannot inspect GUI descriptor: " + str(source) + "; " + str(error)) from error
    failures = []
    for candidate in candidates:
        if not candidate.is_file() or not os.access(candidate, os.X_OK):
            failures.append(str(candidate) + ": missing or not executable")
            continue
        try:
            descriptor = subprocess.run([str(candidate), "--server-info"], capture_output=True,
                                        text=True, timeout=5, check=True)
            server_info = json.loads(descriptor.stdout)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            failures.append(str(candidate) + ": descriptor unavailable: " + str(error))
            continue
        mismatches = [key for key in ("build_variant", "protocol_version", "api_signature", "server_revision")
                      if not server_info.get(key) or server_info.get(key) != client_info.get(key)]
        if server_info.get("build_variant") == "dev" and not mismatches:
            return candidate
        failures.append(str(candidate) + ": requires matching dev descriptors; mismatches=" + ", ".join(mismatches))
    raise RuntimeError("Background smoke requires an executable matching server; "
                       + "; ".join(failures) + "; set WATER_SERVER_BIN or build the sibling server")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--background-seconds", type=float, default=2)
    parser.add_argument("--background-steps", default="2,5,10,20,40", help="comma-separated background durations; empty uses fixed duration/cycles")
    parser.add_argument("--background-mode", choices=("external", "hide", "minimize"), default="external")
    parser.add_argument("--external-app", default="Google Chrome", help="installed app to activate")
    parser.add_argument("--cycles", type=int, default=6)
    parser.add_argument("--workload", choices=("idle", "plain", "ansi", "hyperlinks", "unicode"), default="ansi")
    parser.add_argument("--config-template", type=Path, help="copy appearance/terminal settings into the isolated config")
    parser.add_argument("--rate-mib", type=float, default=1)
    parser.add_argument("--emit-output", choices=("plain", "ansi", "hyperlinks", "unicode"), help=argparse.SUPPRESS)
    parser.add_argument("--emit-seconds", type=float, default=180, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("requires macOS native windows and thread sampling")
    if not 0 < args.background_seconds <= 600:
        parser.error("background duration must be between 0 and 600 seconds")
    if not 1 <= args.cycles <= 20 or not 0 < args.rate_mib <= 16:
        parser.error("cycles must be 1..20 and output rate must be 0..16 MiB/s")
    if args.emit_output:
        print("WATER_OUTPUT_LOAD_START", flush=True)
        deadline = time.monotonic() + args.emit_seconds
        batch = 0
        while time.monotonic() < deadline:
            lines = []
            for row in range(128):
                text = f"WATER_LOAD {batch:09d} {row:03d} " + "0123456789abcdef" * 6
                if args.emit_output == "ansi":
                    text = f"\x1b[{31 + row % 6}m{text}\x1b[0m"
                elif args.emit_output == "hyperlinks":
                    text = f"\x1b]8;;https://example.com/load/{batch}/{row}\x1b\\{text}\x1b]8;;\x1b\\"
                elif args.emit_output == "unicode":
                    text = f"\x1b[1;{31 + row % 6}m{text[:45]} 编译与测试 ==> != -> ✅ ⚠️ 🙂\x1b[0m"
                lines.append(text)
            data = ("\n".join(lines) + "\n").encode()
            started = time.monotonic()
            sys.stdout.buffer.write(data)
            sys.stdout.buffer.flush()
            batch += 1
            time.sleep(max(0, len(data) / (args.rate_mib * 1024 * 1024) - (time.monotonic() - started)))
        return
    try:
        steps = [float(v) for v in args.background_steps.split(",")] if args.background_steps else [args.background_seconds] * args.cycles
    except ValueError:
        parser.error("background steps must be comma-separated numbers")
    if not steps or len(steps) > 20 or any(not 0 < v <= 600 for v in steps):
        parser.error("background steps must contain 1..20 durations between 0 and 600 seconds")
    hold_gui_lock()
    root = Path(__file__).resolve().parent.parent
    water = os.environ.get("WATER_BIN", str(root / "target/go-ui-smoke/water"))
    directory = Path(tempfile.mkdtemp(prefix="water-background.", dir="/tmp"))
    socket = str(directory / "water.sock")
    config = directory / "config.json"
    shell = next((p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).is_file()), None)
    if shell is None:
        raise RuntimeError("zsh is required for the background smoke")
    isolated_config = {
        "startup": {"window_columns": 96, "window_rows": 30},
        "server": {"detached": True, "detach_on_quit": True},
        "shell": {"program": shell, "args": ["-f"]},
    }
    if args.config_template:
        template = json.loads(args.config_template.read_text())
        for key in ("terminal", "ui", "theme", "features"):
            if key in template:
                isolated_config[key] = template[key]
        for key in ("window_columns", "window_rows"):
            if key in template.get("startup", {}):
                isolated_config["startup"][key] = template["startup"][key]
    config.write_text(json.dumps(isolated_config))
    source = Path(water).resolve()
    server = resolve_server(source)
    app = None
    if args.background_mode == "external" or os.environ.get("WATER_SERVER_BIN"):
        # A local, unsigned test bundle lets Launch Services return to this
        # exact GUI after activating a different app. It is never installed.
        app = directory / "Water Background Test.app"
        executables = app / "Contents" / "MacOS"
        executables.mkdir(parents=True)
        shutil.copy2(source, executables / "water-dev")
        shutil.copy2(server, executables / "water-srv-dev")
        (app / "Contents" / "Info.plist").write_bytes(plistlib.dumps({
            "CFBundleIdentifier": f"dev.water.terminal.dev.background-test.{uuid.uuid4()}",
            "CFBundleExecutable": "water-dev", "CFBundleName": "Water Background Test",
            "CFBundlePackageType": "APPL", "NSHighResolutionCapable": True,
        }))
        water = str(executables / "water-dev")
    environment = dict(os.environ, WATER_CONTROL_SOCKET=socket, WATER_CONFIG=str(config))
    environment.pop("WATER_SOCKET", None)
    environment.pop("WATER_GO_DAEMON_CHILD", None)
    environment["WATER_TEST_INSTANCE"] = directory.name
    existing_water = water_processes()
    server_instance = None
    gui = None
    server_pid = None
    records = []
    samplers = []

    def ctl(*command, measured=False, target_socket=None, budget_ms=500):
        started = time.monotonic()
        with subprocess.Popen([water, "ctl", "--socket", target_socket or socket, *command],
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              env=environment) as request:
            try:
                try:
                    stdout, stderr = request.communicate(timeout=budget_ms/1000 if measured else 6)
                except subprocess.TimeoutExpired:
                    if measured:
                        trace = directory / f"slow-{len(records):03d}.sample.txt"
                        samplers.append(subprocess.Popen(
                            ["sample", str(gui.pid), "2", "10", "-file", str(trace)],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
                        print(f"Slow request {command}; sampling GUI {gui.pid}", flush=True)
                        stdout, stderr = request.communicate(timeout=6)
                    else:
                        raise
            finally:
                if request.poll() is None:
                    request.kill()
                    request.communicate()
        elapsed = (time.monotonic() - started) * 1000
        if measured:
            records.append({"command": list(command), "elapsed_ms": round(elapsed, 2),
                            "exit_code": request.returncode, "budget_ms": budget_ms})
            print(f"{command}: {elapsed:.1f} ms", flush=True)
        if request.returncode:
            raise RuntimeError(stderr.decode(errors="replace"))
        if command in (("ping",), ("server", "shutdown")):
            return stdout.decode().strip()
        return json.loads(stdout)

    def owned_server(info):
        pid = info["server_pid"]
        command = subprocess.run(["ps", "-p", str(pid), "-o", "command="],
                                 capture_output=True, text=True, timeout=3, check=True).stdout.strip()
        if (info.get("socket_path") != socket or info.get("build_variant") != "dev"
                or pid in existing_water or socket not in command or str(config) not in command):
            raise OwnershipError("refusing server outside owned background config/socket")
        if info.get("ui_sessions", 0) > 1:
            raise OwnershipError("another GUI attached to the background server; refusing shutdown")
        return pid

    def wait(predicate, description, *, transient=False, measured=False):
        return wait_snapshot(ctl, gui, predicate, description, transient=transient, measured=measured)

    print(f"Artifacts: {directory}", flush=True)
    with (directory / "gui.log").open("w") as log:
        try:
            gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)],
                                   stdout=log, stderr=subprocess.STDOUT, env=environment)
            print(f"owned_gui_pid={gui.pid} socket={socket}", flush=True)
            initial = wait(lambda s: s.get("native_menu", {}).get("ready") and
                           s.get("frame_active_terminal") not in
                           (None, "00000000-0000-0000-0000-000000000000"), "menu and shell", transient=True)
            info = ctl("server", "info")
            server_pid = owned_server(info)
            server_instance = info["instance_id"]
            assert ctl("ping")
            assert info["build_variant"] == "dev" and server_pid != gui.pid
            print(f"owned_server_pid={server_pid}", flush=True)
            (directory / "identity.json").write_text(json.dumps(
                {"gui_pid": gui.pid, "server": info, "binary": water,
                 "binary_sha256": hashlib.sha256(Path(water).read_bytes()).hexdigest(),
                 "steps": steps, "workload": args.workload, "rate_mib": args.rate_mib,
                 "background_mode": args.background_mode, "external_app": args.external_app}, indent=2))
            ctl("ui", "screenshot", "--output", str(directory / "before.png"), measured=True)
            pane = str(initial["frame_focused_pane"])
            terminal = str(initial["frame_active_terminal"])
            if args.workload != "idle":
                command = shlex.join([sys.executable, str(Path(__file__).resolve()),
                                      "--emit-output", args.workload, "--rate-mib", str(args.rate_mib),
                                      "--emit-seconds", str(sum(steps) + 60)])
                ctl("pane", "input", "--pane", pane, "--text", command + "\n")
                ctl("terminal", "contains", "--terminal", terminal,
                    "WATER_LOAD", "--timeout-ms", "5000")
                print(f"Continuous {args.workload} workload at up to {args.rate_mib:g} MiB/s", flush=True)
            for cycle, duration in enumerate(steps):
                before = ctl("ui", "snapshot", measured=True)
                if args.background_mode == "external":
                    # Launch/activate an installed viewer, without injecting any input
                    # into Water. All Water interaction still uses its control API.
                    subprocess.run(["open", "-a", args.external_app], check=True, timeout=5)
                    background = wait(lambda s: s.get("window_focused") is False and
                                      s.get("window_occluded") is True, "Water covered by external application")
                    assert not background["application_hidden"] and not background["window_minimized"]
                elif args.background_mode == "hide":
                    ctl("ui", "menu", "hide-window", measured=True)
                    wait(lambda s: s.get("application_hidden") is True, "native application hidden")
                else:
                    ctl("ui", "menu", "minimize-window", measured=True)
                    wait(lambda s: s.get("window_minimized") is True, "native window minimized")
                # Force offscreen readback while native presentation is disabled.
                # This covers the Metal drawable/pool boundary that previously
                # crashed when another test window occluded a screenshot target.
                ctl("ui", "screenshot", "--output", str(directory / f"background-{cycle + 1}.png"), measured=True)
                hidden_at = time.monotonic()
                print(f"Cycle {cycle + 1}: {args.background_mode} for {duration:g}s with live output", flush=True)
                while time.monotonic() - hidden_at < duration:
                    if gui.poll() is not None:
                        raise RuntimeError("GUI exited in the background")
                    remaining = duration - (time.monotonic() - hidden_at)
                    time.sleep(min(0.1, max(0, remaining)))
                print(f"Cycle {cycle + 1}: restoring while output continues", flush=True)
                restore_started = time.monotonic()
                ctl("ui", "menu", "show-window", measured=True)
                if app is not None and args.background_mode == "external":
                    subprocess.run(["open", "-a", str(app), "--args", "--control-socket", socket,
                                    "--config", str(config)], check=True, timeout=5)
                # AppKit/Dock deminiaturization runs an OS animation transaction
                # on the main thread (~0.7s on macOS). Bound this one transition
                # probe separately; subsequent control requests keep 500ms.
                transition_budget = 1500 if args.background_mode == "minimize" else 500
                state = ctl("ui", "snapshot", measured=True, budget_ms=transition_budget)
                if state.get("window_focused") or state.get("application_hidden") or state.get("window_minimized"):
                    state = wait(lambda s: s.get("window_focused") is False and
                                 not s.get("application_hidden") and not s.get("window_minimized"), "restored visible unfocused application", measured=True)
                assert state["gui_pid"] == gui.pid
                if args.workload != "idle":
                    assert state["active_terminal_last_seq"] > before["active_terminal_last_seq"], "output did not advance in the background"
                ctl("ui", "screenshot", "--output", str(directory / f"restored-{cycle + 1}.png"), measured=True)
                records.append({"command": ["restore-to-render", str(duration)],
                                "elapsed_ms": round((time.monotonic() - restore_started) * 1000, 2), "exit_code": 0})
                ctl("ui", "key", "cmd-h", measured=True)
            if args.workload != "idle":
                # Interrupt only this test's output command before checking shell input.
                ctl("terminal", "send-bytes", "--terminal", terminal, "--hex", "03", measured=True)
            marker = "WATER_BACKGROUND_RESTORE_OK"
            ctl("pane", "input", "--pane", pane,
                "--text", "printf '%s%s\\n' WATER_BACKGROUND_ RESTORE_OK\n", measured=True)
            ctl("terminal", "contains", "--terminal", str(initial["frame_active_terminal"]),
                marker, "--timeout-ms", "5000", measured=True)
            content = ctl("pane", "content", "--pane", str(initial["frame_focused_pane"]), measured=True)
            assert marker in json.dumps(content)
            for _ in range(10):
                ctl("ui", "state", measured=True)
            # Record the native animation probe rather than hiding its latency.
            # End-to-end restoration also includes Launch Services and probes.
            slow = [r for r in records[1:] if r["command"][0] != "restore-to-render"
                    and r["elapsed_ms"] >= r.get("budget_ms", 500)]
            if slow:
                raise RuntimeError(f"{len(slow)} restore requests exceeded their recorded latency budgets; see {directory}")
            print(f"Completed {len(steps)} {args.background_mode}/restore cycles during {args.workload} output, screenshots and terminal round-trip", flush=True)
        finally:
            finish_background(directory, records, samplers,
                lambda: cleanup_background(directory, socket, gui, server_pid, server_instance,
                                           existing_water, ctl, owned_server),
                primary_error=sys.exc_info()[1])


if __name__ == "__main__":
    main()
