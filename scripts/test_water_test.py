"""Non-GUI regressions for process identity and background cleanup failure paths.

Run: python3 -m unittest discover -s scripts -p test_water_test.py
"""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

import water_test

spec = importlib.util.spec_from_file_location(
    "background_smoke", Path(__file__).with_name("go-ui-background-smoke.py"))
background = importlib.util.module_from_spec(spec)
spec.loader.exec_module(background)


class ProcessDiscoveryTests(unittest.TestCase):
    def test_executable_field_selects_process_and_args_preserve_identity(self):
        comm = """11 /tmp/Water Test.app/Contents/MacOS/water-dev
12 /usr/bin/python
13 /tmp/bin/water-server
14 /tmp/bin/water
"""
        args = """11 /tmp/Water Test.app/Contents/MacOS/water-dev --config /tmp/test.json
12 /usr/bin/python /tmp/water --example
13 /tmp/bin/water-server --control-socket /tmp/test.sock
"""
        with patch.object(water_test.subprocess, "run", side_effect=[
                subprocess.CompletedProcess([], 0, comm),
                subprocess.CompletedProcess([], 0, args)]) as run:
            result = water_test.water_processes()
        self.assertEqual(result, {
            11: "/tmp/Water Test.app/Contents/MacOS/water-dev --config /tmp/test.json",
            13: "/tmp/bin/water-server --control-socket /tmp/test.sock"})
        self.assertIn("pid=,comm=", run.call_args_list[0].args[0])
        self.assertIn("pid=,args=", run.call_args_list[1].args[0])


class BackgroundCleanupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="water-helper-test.")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.socket = self.directory / "water.sock"
        self.socket.touch()
        self.gui = Mock()
        self.gui.poll.side_effect = lambda: 0 if self.gui.terminate.called else None
        self.output = contextlib.redirect_stdout(io.StringIO())
        self.output.__enter__()
        self.addCleanup(self.output.__exit__, None, None, None)

    def cleanup(self, ctl, owner):
        with patch.object(background, "water_processes", return_value={}):
            background.cleanup_background(self.directory, str(self.socket), self.gui,
                                          42, "instance", {}, ctl, owner)

    def test_control_failure_still_terminates_owned_gui(self):
        ctl = Mock(side_effect=RuntimeError("connection refused"))
        with self.assertRaisesRegex(RuntimeError, "Background cleanup failed"):
            self.cleanup(ctl, Mock())
        self.gui.terminate.assert_called_once()
        self.gui.wait.assert_called_once_with(timeout=5)
        evidence = json.loads((self.directory / "cleanup.json").read_text())
        self.assertFalse(evidence["cleanup_refused"])
        self.assertIn("connection refused", evidence["errors"])

    def test_ownership_violation_refuses_shutdown(self):
        ctl = Mock(return_value={"instance_id": "instance"})
        with self.assertRaisesRegex(RuntimeError, "Background cleanup failed"):
            self.cleanup(ctl, Mock(side_effect=background.OwnershipError("foreign server")))
        self.gui.terminate.assert_not_called()
        self.assertEqual(ctl.call_count, 1)
        self.assertTrue(json.loads((self.directory / "cleanup.json").read_text())["cleanup_refused"])

    def test_shutdown_transport_failure_still_terminates_gui(self):
        ctl = Mock(side_effect=[{"instance_id": "instance"}, RuntimeError("EOF")])
        with self.assertRaisesRegex(RuntimeError, "Background cleanup failed"):
            self.cleanup(ctl, Mock(return_value=42))
        self.gui.terminate.assert_called_once()

    def test_latency_write_failure_does_not_skip_cleanup(self):
        cleanup = Mock()
        with patch.object(Path, "write_text", side_effect=OSError("disk full")):
            with self.assertRaisesRegex(RuntimeError, "disk full"):
                background.finish_background(self.directory, [], [], cleanup)
        cleanup.assert_called_once()

    def test_sampler_failure_attempts_remaining_samplers_and_cleanup(self):
        failed = Mock()
        failed.wait.side_effect = OSError("sampler failed")
        failed.poll.return_value = 1
        other = Mock()
        cleanup = Mock()
        with self.assertRaisesRegex(RuntimeError, "sampler failed"):
            background.finish_background(self.directory, [], [failed, other], cleanup)
        other.wait.assert_called_once_with(timeout=5)
        cleanup.assert_called_once()

    def test_cleanup_and_evidence_errors_preserve_primary(self):
        primary = AssertionError("primary assertion")
        cleanup = Mock(side_effect=RuntimeError("cleanup failed"))
        with patch.object(Path, "write_text", side_effect=OSError("disk full")):
            with contextlib.redirect_stderr(io.StringIO()):
                background.finish_background(self.directory, [], [], cleanup, primary)
        cleanup.assert_called_once()
        self.assertIn("disk full", primary.__notes__[0])
        self.assertIn("cleanup failed", primary.__notes__[0])


class BackgroundWaitTests(unittest.TestCase):
    def test_ready_wait_does_not_swallow_control_error(self):
        ctl = Mock(side_effect=RuntimeError("broken control"))
        gui = Mock()
        gui.poll.return_value = None
        with self.assertRaisesRegex(RuntimeError, "broken control"):
            background.wait_snapshot(ctl, gui, lambda state: True, "restored", measured=True)
        ctl.assert_called_once_with("ui", "snapshot", target_socket=None, measured=True)

    def test_startup_can_retry_transient_error(self):
        ctl = Mock(side_effect=[RuntimeError("starting"), {"ready": True}])
        gui = Mock()
        gui.poll.return_value = None
        with patch.object(background.time, "sleep"):
            result = background.wait_snapshot(ctl, gui, lambda state: state["ready"],
                                              "startup", transient=True)
        self.assertEqual(result, {"ready": True})


if __name__ == "__main__":
    unittest.main()
