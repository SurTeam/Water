"""Shared ownership, bounded waits and read-only GUI content for Water tests."""
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time


class ControlError(RuntimeError):
    pass


def water_processes():
    result = subprocess.run(["ps", "-axo", "pid=,command="], capture_output=True, text=True, check=True, timeout=3)
    names = {"water", "water-dev", "water-server", "water-srv-dev", "water-test-gui"}
    found = {}
    for line in result.stdout.splitlines():
        parts = line.strip().split(None, 1)
        if len(parts) != 2:
            continue
        command = parts[1]
        if command.startswith("/Applications/Water") or Path(command.split()[0]).name in names:
            found[int(parts[0])] = command
    return found


class WaterGUI:
    def __init__(self, prefix="water-test.", config=None, binary=None, variant="dev"):
        self.variant = variant
        if variant not in ("dev", "release"):
            raise ValueError("test variant must be dev or release")
        self.root = Path(__file__).resolve().parent.parent
        self.binary = str(binary or os.environ.get("WATER_BIN", self.root / "target/go-ui-smoke/water"))
        self.directory = Path(tempfile.mkdtemp(prefix=prefix, dir="/tmp"))
        self.socket = str(self.directory / "water.sock")
        self.shell = next((p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).is_file()), None)
        if self.shell is None:
            raise RuntimeError("zsh is required for the GUI fixture")
        self.config = {"startup": {"default_cwd": str(self.directory)},
                       "shell": {"program": self.shell, "args": ["-f"]},
                       "server": {"detached": False, "detach_on_quit": False}}
        for group, values in (config or {}).items():
            self.config.setdefault(group, {}).update(values)
        self.gui = None
        self.log = None
        self.window = None
        self.previous_sigterm = None
        self.existing_water = water_processes()
        self.server_instances = {}

    def __enter__(self):
        self.previous_sigterm = signal.getsignal(signal.SIGTERM)
        signal.signal(signal.SIGTERM, self.interrupted)
        config = self.directory / "config.json"
        config.write_text(json.dumps(self.config))
        self.log = (self.directory / "gui.log").open("w")
        env = dict(os.environ, WATER_TEST_INSTANCE=self.directory.name,
                   WATER_CONTROL_SOCKET=self.socket, WATER_CONFIG=str(config))
        env.pop("WATER_SOCKET", None)
        env.pop("WATER_GO_DAEMON_CHILD", None)
        self.gui = subprocess.Popen([self.binary, "--control-socket", self.socket, "--config", str(config)],
                                    stdout=self.log, stderr=subprocess.STDOUT, env=env)
        self.ownership = {"gui_pid": self.gui.pid, "socket": self.socket, "config": str(config)}
        self.save("ownership.json", self.ownership)
        try:
            self.wait(lambda: self.ctl("ping"), "control readiness", transient=True)
            snapshot = self.wait(lambda: self.ctl("ui", "snapshot"), "GUI snapshot", transient=True)
            if snapshot.get("test_instance") != self.directory.name:
                raise RuntimeError("GUI identity is not the owned test instance")
            self.window = snapshot["window_id"]
            self.wait(lambda: self.ctl("ui", "snapshot").get("frame_focused_pane"), "first GUI frame")
            info = self.ctl("server", "info")
            self.record_server_info(info)
            self.ownership.update(server_pid=info["server_pid"], window_id=self.window,
                                  existing_water=self.existing_water)
            self.save("ownership.json", self.ownership)
            return self
        except BaseException as error:
            self.save("failure.txt", str(error))
            self.close()
            raise

    def __exit__(self, error_type, error, traceback):
        if error is not None:
            self.save("failure.txt", str(error))
            try:
                snapshot = self.ctl("ui", "snapshot")
                self.save("failure-ui.json", snapshot)
                pane = snapshot.get("frame_focused_pane")
                if pane:
                    self.save("failure-content.json", self.content(pane, 0, 256))
            except (ControlError, subprocess.TimeoutExpired):
                pass
        self.close()

    def save(self, name, value):
        path = self.directory / name
        path.write_text(value if isinstance(value, str) else json.dumps(value, indent=2))
        return path

    def ctl(self, *args):
        result = subprocess.run([self.binary, "ctl", "--socket", self.socket, *map(str, args)],
                                capture_output=True, text=True, timeout=5)
        if result.returncode:
            raise ControlError(result.stderr.strip() or result.stdout.strip())
        if args == ("ping",) or args == ("server", "shutdown"):
            return {"text": result.stdout.strip()}
        try:
            return json.loads(result.stdout)
        except json.JSONDecodeError as error:
            raise ControlError("control command returned invalid JSON: " + " ".join(map(str, args))) from error

    def wait(self, predicate, description, timeout=8, transient=False):
        deadline = time.monotonic() + timeout
        last_error = None
        while time.monotonic() < deadline:
            if self.gui is not None and self.gui.poll() is not None:
                raise RuntimeError("GUI exited; artifacts=" + str(self.directory))
            try:
                value = predicate()
                if value:
                    return value
            except (ControlError, subprocess.TimeoutExpired) as error:
                if not transient:
                    raise
                last_error = error
            time.sleep(.025)
        raise RuntimeError("Timeout: " + description + "; artifacts=" + str(self.directory)
                           + ("; last error=" + str(last_error) if last_error else ""))

    def content(self, pane, start_row=None, rows=None):
        args = ["ui", "content", "--pane", pane, "--window", self.window]
        if start_row is not None:
            args += ["--start-row", start_row]
        if rows is not None:
            args += ["--rows", rows]
        result = self.ctl(*args)
        if result["source"] != "gui-client" or result["window_id"] != self.window or result["pane_id"] != pane:
            raise RuntimeError("content query returned the wrong GUI/window/pane")
        return result

    def history(self, pane):
        # Each page is bounded. If output/trim changes, retry from the beginning
        # rather than joining rows from different terminal states.
        for _ in range(3):
            first = self.content(pane, 0, 256)
            if first["synchronized_output"]:
                self.wait(lambda: not self.content(pane)["synchronized_output"], "synchronized redraw finished")
                continue
            signature = tuple(first[k] for k in ("last_seq", "trimmed_lines", "alt_screen", "total_rows"))
            lines = first["lines"]
            while len(lines) < first["total_rows"]:
                page = self.content(pane, len(lines), 256)
                if signature != tuple(page[k] for k in ("last_seq", "trimmed_lines", "alt_screen", "total_rows")):
                    break
                if not page["lines"]:
                    raise RuntimeError("history pagination made no progress")
                lines += page["lines"]
            else:
                return "\n".join(lines)
        raise RuntimeError("terminal changed during history query; artifacts=" + str(self.directory))

    def screenshot(self, name):
        path = self.directory / name
        self.ctl("ui", "screenshot", "--output", path)
        return path

    @staticmethod
    def interrupted(signum, frame):
        raise KeyboardInterrupt("test interrupted; closing owned GUI")

    def record_server_info(self, info):
        if info.get("socket_path") != self.socket or info.get("build_variant") != self.variant:
            raise RuntimeError("refusing a server outside the owned test socket/variant")
        pid = info["server_pid"]
        command = subprocess.run(["ps", "-p", str(pid), "-o", "command="], capture_output=True, text=True, timeout=3).stdout.strip()
        if pid != self.gui.pid and not (self.socket in command and str(self.directory / "config.json") in command):
            raise RuntimeError("server PID does not belong to this test config/socket")
        if pid in self.existing_water:
            raise RuntimeError("refusing to adopt a pre-existing Water process")
        self.server_instances[info.get("instance_id", str(pid))] = pid
        self.ownership["owned_server_instances"] = self.server_instances
        self.save("ownership.json", self.ownership)

    def close(self):
        if self.previous_sigterm is not None:
            signal.signal(signal.SIGTERM, self.previous_sigterm)
            self.previous_sigterm = None
        if self.gui is None:
            return
        cleanup_error = None
        try:
            if Path(self.socket).exists():
                info = self.ctl("server", "info")
                self.record_server_info(info)
                if info.get("ui_sessions", 0) > 1:
                    raise RuntimeError("another window attached to the test server; refusing shutdown")
                self.ctl("server", "shutdown")
        except (ControlError, subprocess.TimeoutExpired):
            pass
        except RuntimeError as error:
            cleanup_error = error
        if cleanup_error:
            self.save("cleanup.json", {"guard_error": str(cleanup_error), "cleanup_refused": True,
                                       "gui_pid": self.gui.pid, "socket": self.socket})
            raise cleanup_error
        if self.gui.poll() is None:
            self.gui.terminate()
        try:
            self.gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.gui.kill()
            self.gui.wait(timeout=5)
        if self.log:
            self.log.close()
        current = water_processes()
        preserved = all(current.get(pid) == command for pid, command in self.existing_water.items())
        self.save("cleanup.json", {"gui_exited": self.gui.poll() is not None,
                                   "socket_removed": not Path(self.socket).exists(),
                                   "existing_water_preserved": preserved,
                                   "existing_water_before": self.existing_water,
                                   "existing_water_after": {pid: current.get(pid) for pid in self.existing_water},
                                   "guard_error": str(cleanup_error) if cleanup_error else None})
        export = os.environ.get("WATER_TEST_EVIDENCE_DIR")
        if export:
            shutil.copytree(self.directory, Path(export) / self.directory.name, dirs_exist_ok=True)
        print("PASS owned GUI cleanup; existing Water preserved=" + str(preserved)
              + "; existing PIDs=" + ",".join(map(str, self.existing_water))
              + "; artifacts=" + str(self.directory), flush=True)
        self.gui = None
        if cleanup_error:
            raise cleanup_error
        if not preserved:
            raise RuntimeError("existing Water process changed during the test; inspect ownership/cleanup evidence")
