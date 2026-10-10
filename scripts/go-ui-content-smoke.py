#!/usr/bin/env python3
import os
"""Verify GUI content pagination and absence of viewport/selection side effects."""
from water_test import ControlError, WaterGUI

os.environ["WATER_TEST_UNFOCUSED"] = "1"

with WaterGUI("water-content.") as test:
    pane = test.ctl("ui", "snapshot")["frame_focused_pane"]
    test.ctl("pane", "input", "--pane", pane, "--text", "printf 'CONTENT_ROW_%03d\\n' {0..399}\n")
    test.wait(lambda: "CONTENT_ROW_399" in test.content(pane)["text"], "history fixture")
    before = test.content(pane)
    history = test.history(pane)
    after = test.content(pane)
    for n in range(400):
        assert history.count(f"CONTENT_ROW_{n:03d}") == 1, "pagination lost or duplicated a row"
    assert before["y_disp"] == after["y_disp"] and before["selection"] == after["selection"]
    assert not after["selection"]["Active"]
    grid = next(g for g in test.ctl("ui", "snapshot")["terminal_grids"] if g["pane_id"] == pane)
    x0, y0, x1, y1 = grid["rect"]
    test.ctl("ui", "wheel", "--x", (x0+x1)/2, "--y", (y0+y1)/2, "--dy", -200)
    test.wait(lambda: test.content(pane)["y_disp"] < test.content(pane)["y_base"], "history viewport")
    viewport = test.content(pane)
    assert viewport["start_row"] == viewport["y_disp"]
    history_again = test.history(pane)
    assert history_again == history
    assert test.content(pane)["y_disp"] == viewport["y_disp"], "reading history scrolled the GUI"
    for options in (("--rows", 257), ("--start-row", -1), ("--start-row", after["total_rows"]+1)):
        try:
            test.ctl("ui", "content", "--pane", pane, *options)
        except ControlError:
            pass
        else:
            raise AssertionError("out-of-bounds content query accepted")
    test.save("viewport.json", viewport)
    test.save("history.txt", history)
    test.screenshot("viewport.png")
    print("PASS 400-row GUI pagination, scrolled viewport, bounded queries and unchanged selection/scroll; artifacts=" + str(test.directory), flush=True)
