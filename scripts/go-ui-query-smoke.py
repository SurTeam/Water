#!/usr/bin/env python3
"""Check OSC/CSI/DCS replies through an owned native GUI and real PTY."""
import json
import shlex
import subprocess
import sys

from water_test import WaterGUI

with WaterGUI("water-queries.", config={"startup": {"window_columns": 100, "window_rows": 32}}) as test:
    directory = test.directory
    root = test.root
    ctl = test.ctl

    def wait(predicate, description):
        return test.wait(lambda: (lambda state: state if predicate(state) else None)(
            ctl("ui", "snapshot")), description, timeout=10)

    fixture = root / "scripts/clipboard-fixture.swift"
    saved = directory / "clipboard.json"
    subprocess.run(["swift", str(fixture), "save", str(saved)], check=True, timeout=20)
    try:
        state = wait(lambda s:s.get("terminal_grids"),"GUI grid")
        for phase in range(2):
            pane = str(state["frame_focused_pane"])
            grid = next(g for g in state["terminal_grids"] if str(g["pane_id"])==pane)
            # PTY size reflects the configured terminal rows/columns. A newly
            # launched InitUnfocused window may be partially off-screen or not
            # yet resized to the full framebuffer geometry, so do not use the
            # visible pane row count as the terminal-size query expectation.
            expected = dict(grid, rows=grid["terminal_rows"], columns=grid["terminal_columns"],
                            screen_size_pixels=state["screen_size_pixels"], frame_size=state["frame_size"])
            scale = state["display_scale"]
            expected["window_position_pixels"] = [int(p*scale+.5) for p in state["window_position"]]
            expected["text_position_pixels"] = [a+b for a,b in zip(expected["window_position_pixels"],grid["rect"][:2])]
            command = shlex.join([sys.executable,str(root/"scripts/terminal-query-probe.py"),json.dumps(expected)])+"\n"
            ctl("pane","input","--pane",pane,"--text",command)
            wait(lambda s:"WATER_QUERY_PASS 18 queries" in ctl("pane","content","--pane",pane)["text"],"PTY query replies")
            wait(lambda s:any(g.get("working_directory_uri")=="file://localhost/tmp/water-query-test" for g in s["terminal_grids"]),"OSC7 directory")
            wait(lambda s:subprocess.run(["pbpaste"],capture_output=True,timeout=3).stdout==b"WATER_OSC52_COPY_TEST","OSC52 system clipboard copy")
            ctl("ui","screenshot","--output",str(directory/f"queries-{phase}.png"))
            print(f"PASS phase {phase}: 18 real PTY replies, OSC7 directory, OSC52 copy; grid={grid['columns']}x{grid['rows']}",flush=True)
            if phase==0:
                ctl("pane","split","--pane",pane,"--down")
                state = wait(lambda s:len(s["terminal_grids"])==2 and s["frame_focused_pane"]!=pane,"split updates terminal dimensions")
    finally:
        # The surrounding WaterGUI context still closes if restoration fails.
        primary_error = sys.exc_info()[1]
        try:
            subprocess.run(["swift", str(fixture), "restore", str(saved)], check=True, timeout=20)
            saved.unlink()
        except Exception as restore_error:
            if primary_error is None:
                raise
            if hasattr(primary_error, "add_note"):
                primary_error.add_note("Clipboard restore also failed: " + str(restore_error))
            print("FAIL clipboard restoration: " + str(restore_error), file=sys.stderr, flush=True)
