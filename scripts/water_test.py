"""Shared ownership, bounded waits and read-only GUI content for Water tests."""
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time


class ControlError(RuntimeError):
    pass


class WaterGUI:
    def __init__(self, prefix="water-test.", config=None, binary=None):
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

    def __enter__(self):
        self.previous_sigterm = signal.getsignal(signal.SIGTERM)
        signal.signal(signal.SIGTERM, self.interrupted)
        config = self.directory / "config.json"
        config.write_text(json.dumps(self.config))
        self.log = (self.directory / "gui.log").open("w")
        self.gui = subprocess.Popen([self.binary, "--control-socket", self.socket, "--config", str(config)],
                                    stdout=self.log, stderr=subprocess.STDOUT)
        self.ownership = {"gui_pid": self.gui.pid, "socket": self.socket, "config": str(config)}
        self.save("ownership.json", self.ownership)
        try:
            self.wait(lambda: self.ctl("ping"), "control readiness", transient=True)
            snapshot = self.wait(lambda: self.ctl("ui", "snapshot"), "GUI snapshot", transient=True)
            self.window = snapshot["window_id"]
            self.wait(lambda: self.ctl("ui", "snapshot").get("frame_focused_pane"), "first GUI frame")
            self.ownership.update(server_pid=self.ctl("server", "info")["server_pid"], window_id=self.window)
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

    def close(self):
        if self.previous_sigterm is not None:
            signal.signal(signal.SIGTERM, self.previous_sigterm)
            self.previous_sigterm = None
        if self.gui is None:
            return
        try:
            if Path(self.socket).exists():
                self.ctl("server", "shutdown")
        except (ControlError, subprocess.TimeoutExpired):
            pass
        if self.gui.poll() is None:
            self.gui.terminate()
        try:
            self.gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.gui.kill()
            self.gui.wait(timeout=5)
        if self.log:
            self.log.close()
        self.save("cleanup.json", {"gui_exited": self.gui.poll() is not None,
                                   "socket_removed": not Path(self.socket).exists()})
        self.gui = None
