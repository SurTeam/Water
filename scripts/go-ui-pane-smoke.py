#!/usr/bin/env python3
"""Exercise pane MRU close, cell resize and unfocused scrolling in an owned GUI."""
from water_test import WaterGUI

with WaterGUI("water-pane-ui.", config={"startup": {"window_columns": 120, "window_rows": 40}}) as test:
    def snapshot():
        return test.ctl("ui", "snapshot")

    def grids():
        return snapshot()["terminal_grids"]

    def tree():
        return test.ctl("state")["workspace"]["tabs"][0]["tree"]

    def key(spec):
        assert test.ctl("ui", "key", spec)["handled"], spec

    def focus(pane):
        g = next(g for g in grids() if g["pane_id"] == pane)
        x0, y0, x1, y1 = g["rect"]
        assert test.ctl("ui", "click", "--x", (x0+x1)//2, "--y", (y0+y1)//2)["handled"]
        test.wait(lambda: snapshot()["frame_focused_pane"] == pane, "pane focus")

    first = snapshot()["frame_focused_pane"]
    assert snapshot()["window_focused"] is False
    key("cmd-\\")
    test.wait(lambda: len(grids()) == 2, "horizontal split")
    second = snapshot()["frame_focused_pane"]
    assert first != second
    before = next(g for g in grids() if g["pane_id"] == second)["columns"]
    key("cmd-shift-h")
    test.wait(lambda: next(g for g in grids() if g["pane_id"] == second)["columns"] == before-1, "narrow one column")
    key("cmd-shift-l")
    test.wait(lambda: next(g for g in grids() if g["pane_id"] == second)["columns"] == before, "widen one column")
    key("cmd--")
    test.wait(lambda: len(grids()) == 3, "nested vertical split")
    third = snapshot()["frame_focused_pane"]
    before_rows = next(g for g in grids() if g["pane_id"] == third)["rows"]
    key("cmd-shift-j")
    test.wait(lambda: next(g for g in grids() if g["pane_id"] == third)["rows"] == before_rows-1, "shorten one row")
    key("cmd-shift-k")
    test.wait(lambda: next(g for g in grids() if g["pane_id"] == third)["rows"] == before_rows, "grow one row")
    assert not any(h["kind"] == "remote_field" for h in snapshot()["automation_hits"])
    # A burst follows the repeat path without waiting for each model projection.
    before_ratio = tree()["second"]["ratio"]
    for _ in range(8):
        key("cmd-shift-j")
    test.wait(lambda: next(g for g in grids() if g["pane_id"] == third)["rows"] == before_rows-8, "eight additive row steps")
    test.save("resized.json", {"before_ratio": before_ratio, "tree": tree(), "gui": snapshot()})
    test.screenshot("resized-unfocused.png")
    focus(second)
    focus(third)
    key("cmd-shift-w")
    test.wait(lambda: len(grids()) == 2 and snapshot()["frame_focused_pane"] == second, "close restores previous pane instead of first")
    key("cmd-shift-w")
    test.wait(lambda: len(grids()) == 1 and snapshot()["frame_focused_pane"] == first, "close restores remaining pane")
    # Scroll remains available with the native window still unfocused.
    test.ctl("pane", "input", "--pane", first, "--text", "printf 'PANE_SCROLL_%03d\\n' {0..199}\n")
    test.wait(lambda: "PANE_SCROLL_199" in test.content(first)["text"], "scroll fixture output")
    g = grids()[0]
    x0, y0, x1, y1 = g["rect"]
    test.ctl("ui", "wheel", "--x", (x0+x1)//2, "--y", (y0+y1)//2, "--dy", -100)
    test.wait(lambda: test.content(first)["y_disp"] < test.content(first)["y_base"], "unfocused scrolling")
    assert snapshot()["window_focused"] is False
    test.save("unfocused-scroll.json", test.content(first))
    key("cmd-shift-w")
    test.wait(lambda: len(grids()) == 0 and snapshot().get("frame_focused_pane") == "00000000-0000-0000-0000-000000000000", "final pane in final tab closes")
    state = test.ctl("state")
    assert not state["workspace"]["tabs"] and state["workspace"]["active_tab"] is None
    test.save("empty-workspace.json", state)
    test.screenshot("empty-workspace.png")
    print("PASS real GUI one-cell width/height resize, eight additive steps, MRU close, final-pane close and unfocused scrolling; artifacts="+str(test.directory), flush=True)
