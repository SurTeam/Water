#!/usr/bin/env python3
"""Verify selection boundaries, Shift extension and remote modal pixels in an owned GUI."""
import os
from PIL import Image
from water_test import WaterGUI

config = {"startup": {"window_columns": 100, "window_rows": 36},
          "theme": {"chrome_background": "#808080", "terminal_background": "#808080", "pane_background": "#808080", "ui_foreground": "#ffffff"}}
with WaterGUI("water-selection-modal.", config=config, variant=os.environ.get("WATER_TEST_VARIANT", "dev")) as test:
    state = test.ctl("ui", "snapshot")
    pane = state["frame_focused_pane"]
    test.ctl("ui", "key", r"text:printf '\033[2J\033[HABCDEFGH\n'")
    test.ctl("ui", "key", "enter")
    test.wait(lambda: test.content(pane)["text"].startswith("ABCDEFGH"), "selection fixture output")
    def grid():
        return next(g for g in test.ctl("ui", "snapshot")["terminal_grids"] if g["pane_id"] == pane)
    g = grid()
    x, y = g["rect"][:2]
    cw, lh = g["cell_width"], g["cell_height"]
    yy = y + lh//2
    def drag(a, b):
        assert test.ctl("ui", "drag", "--x", x+a*cw, "--y", yy, "--to-x", x+b*cw, "--to-y", yy)["handled"]
        return grid()["selection"]
    s = drag(1.49, 4.49)
    assert s["AnchorCol"] == 1 and s["FocusCol"] == 4 and s["Boundaries"], s
    s = drag(1.51, 4.51)
    assert s["AnchorCol"] == 2 and s["FocusCol"] == 5, s
    s = drag(4.49, 1.49)
    assert s["AnchorCol"] == 4 and s["FocusCol"] == 1, s
    drag(1, 4)
    assert test.ctl("ui", "click", "--x", x+6*cw, "--y", yy, "--shift")["handled"]
    s = grid()["selection"]
    assert s["AnchorCol"] == 1 and s["FocusCol"] == 6, s
    test.ctl("ui", "click", "--x", x, "--y", yy, "--shift")
    s = grid()["selection"]
    assert s["AnchorCol"] == 1 and s["FocusCol"] == 0, s
    test.save("shift-selection.json", s)
    test.screenshot("shift-selection.png")
    test.ctl("ui", "click", "--x", x+2*cw, "--y", yy, "--click-count", 2)
    s = grid()["selection"]
    assert s["Active"] and not s["Boundaries"] and s["AnchorCol"] == 0 and s["FocusCol"] == 7, s
    test.ctl("ui", "click", "--x", x+6*cw, "--y", yy)
    assert not grid()["selection"]["Active"]
    baseline = Image.open(test.screenshot("baseline.png")).convert("RGB")
    assert test.ctl("ui", "key", "cmd-shift-k")["handled"]
    def remote():
        s = test.ctl("ui", "snapshot")
        return s if any(h["kind"] == "remote_field" for h in s["automation_hits"]) else None
    state = test.wait(remote, "remote dialog")
    test.save("remote.json", state)
    img = Image.open(test.screenshot("remote.png")).convert("RGB")
    scale = state["display_scale"]
    height = min(round(270*scale), img.height-round(96*scale))
    bottom = (img.height+height)//2
    near = img.getpixel((img.width//2, bottom+round(3*scale)))
    far = img.getpixel((img.width//2, bottom+round(20*scale)))
    assert near == far and all(0 < v < 128 for v in near), (near, far)
    field = next(h["rect"] for h in state["automation_hits"] if h["kind"] == "remote_field")
    x0, y0, x1, y1 = field
    ys = [py for py in range(y0+round(4*scale), y1-round(4*scale))
          for px in range(x0+round(10*scale), min(x1-round(10*scale), x0+round(60*scale)))
          if min(img.getpixel((px,py))) > 190]
    assert ys, "remote field text not rendered"
    top_gap, bottom_gap = min(ys)-y0, y1-1-max(ys)
    assert abs(top_gap-bottom_gap) <= round(4*scale), (top_gap,bottom_gap)
    test.save("assertions.json", {"remote_near_edge": near, "remote_far_edge": far,
                                 "field_top_gap": top_gap, "field_bottom_gap": bottom_gap})
    test.ctl("ui", "key", "escape")
    test.wait(lambda: not remote(), "remote closed")
    print("PASS real GUI midpoint boundaries, reverse drag, Shift extension/crossing, double-click, shadowless remote and centered field; artifacts="+str(test.directory), flush=True)
