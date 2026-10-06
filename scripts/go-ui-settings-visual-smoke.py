#!/usr/bin/env python3
"""Verify settings coverage, modal pixels and macOS focus through owned GUIs."""
import os
import platform
from PIL import Image
from water_test import WaterGUI


def snapshot(test):
    return test.ctl("ui", "snapshot")


def click(test, label):
    state = test.wait(lambda: (lambda s: s if any(h.get("label") == label for h in s["automation_hits"]) else None)(snapshot(test)), label)
    hit = next(h for h in state["automation_hits"] if h.get("label") == label)
    x0, y0, x1, y1 = hit["rect"]
    assert test.ctl("ui", "click", "--x", (x0+x1)/2, "--y", (y0+y1)/2)["handled"]


def image(test, name):
    return Image.open(test.screenshot(name)).convert("RGB")


def control_colors(state, pixels):
    hits = [h for h in state["automation_hits"] if h["kind"] in ("window_close", "window_minimize", "window_maximize")]
    assert len(hits) == 3
    return [pixels.getpixel(((h["rect"][0]+h["rect"][2])//2, (h["rect"][1]+h["rect"][3])//2)) for h in hits]


variant = os.environ.get("WATER_TEST_VARIANT", "dev")
config = {"startup": {"window_columns": 100, "window_rows": 36},
          "theme": {"chrome_background": "#808080", "terminal_background": "#808080", "pane_background": "#808080"},
          "ui": {"language": "zh-Hans"}}
with WaterGUI("water-settings-visual.", config=config, variant=variant) as test:
    initial = test.wait(lambda: (lambda s: s if s.get("window_focused") and s.get("native_menu", {}).get("ready") else None)(snapshot(test)), "focused native GUI")
    test.save("initial.json", initial)
    before = image(test, "before.png")
    point = (before.width-40, before.height-40)
    baseline = before.getpixel(point)
    assert baseline == (128, 128, 128), baseline
    assert test.ctl("ui", "key", "cmd-," if platform.system() == "Darwin" else "ctrl-,")["handled"]
    settings = test.wait(lambda: (lambda s: s if s.get("settings_visible") else None)(snapshot(test)), "settings opened")
    fields = {f["name"]: f for f in settings["settings_fields"]}
    assert len(fields) == len(settings["settings_fields"]), "duplicate settings controls"
    for name, label in (("SidebarConnectionBackground", "主机卡片背景颜色"), ("SidebarConnectionActiveBackground", "活动主机卡片背景颜色")):
        assert fields["Theme."+name]["label"] == label
        assert fields["Theme."+name]["section"] == "主机卡片颜色"
    dimmed = image(test, "settings.png").getpixel(point)
    assert all(0 < b < a for a, b in zip(baseline, dimmed)), (baseline, dimmed)
    click(test, "update:open")
    test.wait(lambda: snapshot(test)["update_visible"], "update opened above settings")
    assert image(test, "update-over-settings.png").getpixel(point) == dimmed, "stacked modal darkens background twice"
    click(test, "update:close")
    test.wait(lambda: (lambda s: s["settings_visible"] and not s["update_visible"])(snapshot(test)), "return to settings")
    click(test, "category:Theme")
    def host_field():
        state = snapshot(test)
        label = "field:Theme.SidebarConnectionBackground"
        if any(h.get("label") == label for h in state["automation_hits"]):
            return state
        width, height = state["frame_size"]
        test.ctl("ui", "wheel", "--x", width*.65, "--y", height*.5, "--dy", 176)
        return None
    test.save("host-background-visible.json", test.wait(host_field, "host card background rendered in settings"))
    assert test.ctl("ui", "key", "escape")["handled"]
    test.wait(lambda: not snapshot(test)["settings_visible"], "settings closed")
    test.ctl("ui", "menu", "check-updates")
    update = test.wait(lambda: (lambda s: s if s.get("update_visible") else None)(snapshot(test)), "standalone update opened")
    pixels = image(test, "update.png")
    scale = update["display_scale"]
    height = min(round(320*scale), pixels.height-round(96*scale))
    bottom = (pixels.height+height)//2
    near = pixels.getpixel((pixels.width//2, bottom+max(2, round(3*scale))))
    far = pixels.getpixel((pixels.width//2, bottom+round(20*scale)))
    assert near == far == dimmed, (near, far, dimmed)
    assert test.ctl("ui", "key", "escape")["handled"]
    test.wait(lambda: not snapshot(test)["update_visible"], "update closed")
    if platform.system() == "Darwin":
        focused = control_colors(snapshot(test), image(test, "focused.png"))
        assert len(set(focused)) == 3, focused
        with WaterGUI("water-focus-peer.", config=config, variant=variant) as peer:
            test.wait(lambda: not snapshot(test)["window_focused"], "owned peer takes native focus")
            inactive_state = snapshot(test)
            inactive = control_colors(inactive_state, image(test, "inactive.png"))
            assert len(set(inactive)) == 1, inactive
            assert max(inactive[0])-min(inactive[0]) <= 2, inactive
            test.save("inactive.json", inactive_state)
        test.ctl("ui", "menu", "show-window")
        test.wait(lambda: snapshot(test)["window_focused"], "focus restored")
        restored = control_colors(snapshot(test), image(test, "restored.png"))
        assert restored == focused, (restored, focused)
    test.save("assertions.json", {"settings_fields": len(fields), "baseline": baseline, "settings_dimmed": dimmed,
                                  "update_near_edge": near, "update_far_edge": far})
    print("PASS settings reachability, localized host backgrounds, settings dimming, shadowless update and native focus colors; artifacts="+str(test.directory), flush=True)
