#!/usr/bin/env python3
"""Verify long-run overlays and stable label/hit geometry in an owned GUI."""
from PIL import Image
from water_test import WaterGUI

with WaterGUI("water-long-run.", config={"ui": {
    "shell_long_run_notification_seconds": 1,
    "system_notifications": False,
    "sidebar_workspace_alignment": "center",
}}) as test:
    state = test.ctl("state")
    workspace = state["workspaces"][0]
    tab = workspace["tabs"][0]["id"]
    test.ctl("tab", "rename", tab, "Long task")
    test.wait(lambda: any(h["kind"] == "tab" and h["label"] == "Long task"
                          for h in test.ctl("ui", "snapshot")["automation_hits"]), "fixed tab title")
    before = test.ctl("ui", "snapshot")
    pane = before["frame_focused_pane"]
    tab_hit = next(h for h in before["automation_hits"] if h["kind"] == "tab" and h["id"] == tab)
    ws_hit = next(h for h in before["automation_hits"] if h["kind"] == "workspace" and h["id"] == workspace["id"])
    baseline = Image.open(test.screenshot("before.png")).convert("RGB")
    rect = tab_hit["rect"]
    dot_edge = round(8 * before["display_scale"])
    dot_box = (rect[0], rect[1], rect[0] + dot_edge, rect[1] + dot_edge)
    def green_count(image):
        return sum(g > r + 30 and g > b + 15 for r, g, b in image.crop(dot_box).get_flattened_data())
    assert green_count(baseline) == 0
    test.ctl("pane", "input", "--pane", pane, "--text", "sleep 30\n")
    def visible():
        image = Image.open(test.screenshot("running.png")).convert("RGB")
        return image if green_count(image) >= 6 else None
    running = test.wait(visible, "long-running green dot", timeout=8)
    after = test.ctl("ui", "snapshot")
    for hit in (tab_hit, ws_hit):
        current = next(h for h in after["automation_hits"] if h["kind"] == hit["kind"] and h["id"] == hit["id"])
        assert current["rect"] == hit["rect"], "overlay moved hit geometry"
        assert current["label"] == hit["label"], "overlay changed label"
    wr = ws_hit["rect"]
    edge = round(30 * before["display_scale"])
    count_box = (wr[2] - edge, wr[1], wr[2], wr[3])
    assert baseline.crop(count_box).tobytes() != running.crop(count_box).tobytes(), "workspace count not drawn"
    # The central name pixels remain identical while the badge floats at the edge.
    name_box = (wr[0] + edge, wr[1], wr[2] - edge, wr[3])
    assert baseline.crop(name_box).tobytes() == running.crop(name_box).tobytes(), "workspace name moved"
    test.ctl("ui", "key", "ctrl-c")
    def cleared():
        image = Image.open(test.screenshot("finished.png")).convert("RGB")
        return green_count(image) == 0 and image.crop(count_box).tobytes() == baseline.crop(count_box).tobytes()
    test.wait(cleared, "finished command overlays cleared")
    test.save("layout.json", {"before": before, "running": after})
    print("PASS: long-run dot/count appear, preserve layout, and clear; artifacts=" + str(test.directory), flush=True)
