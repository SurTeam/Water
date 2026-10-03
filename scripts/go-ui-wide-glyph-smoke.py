#!/usr/bin/env python3
"""Check actual wide-icon pixels and cursor rendering in isolated native GUIs."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
from PIL import Image

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-app/dev/water"))
directory = Path(tempfile.mkdtemp(prefix="water-wide-glyph.", dir="/tmp"))
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())

for size in (8, 16, 32):
    socket = str(directory / f"water-{size}.sock")
    config = directory / f"config-{size}.json"
    config.write_text(json.dumps({"startup": {"window_columns": 48, "window_rows": 18},
        "terminal": {"font_family": "Sarasa Term SC Nerd Font", "font_size": size, "line_height": size},
        "theme": {"terminal_background": "#000000", "terminal_foreground": "#ffffff"},
        "shell": {"program": shell, "args": ["-f"]}, "server": {"detached": False}}))
    log = (directory / f"gui-{size}.log").open("w")
    gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], stdout=log, stderr=subprocess.STDOUT)
    print(f"owned_gui_pid={gui.pid} socket={socket}", flush=True)

    def ctl(*args):
        result = subprocess.run([water, "ctl", "--socket", socket, *args], capture_output=True, timeout=6)
        if result.returncode:
            raise RuntimeError(result.stderr.decode(errors="replace"))
        return json.loads(result.stdout) if args != ("server", "shutdown") else result.stdout.decode()

    def wait(predicate, description):
        deadline = time.monotonic() + 8
        state = {}
        while time.monotonic() < deadline:
            if gui.poll() is not None:
                raise RuntimeError((directory / f"gui-{size}.log").read_text())
            try:
                state = ctl("ui", "snapshot")
                if predicate(state):
                    return state
            except (RuntimeError, subprocess.SubprocessError):
                pass
            time.sleep(.02)
        (directory / f"failure-{size}.json").write_text(json.dumps(state, indent=2))
        raise RuntimeError(f"Timeout {description}; artifacts={directory}")

    try:
        state = wait(lambda s: s.get("terminal_grids") and "nerd" in s.get("terminal_font", {}).get("path", "").lower(), "resolved font and grid")
        server_pid = ctl("server", "info")["server_pid"]
        print(f"owned_server_pid={server_pid}", flush=True)
        pane = str(state["frame_focused_pane"])
        body = "\\033[2J\\033[H\\033[?25l\\033[0;38;2;255;255;255mWATER_WIDE_GLYPH_TEST"
        body += "\\033[2;1HA B\\033[3;1H\\033[1mA B\\033[0;38;2;255;255;255m"
        body += "\\033[4;1H\\033[3mA B\\033[0;38;2;255;255;255m"
        body += "\\033[5;1HA界B\\033[6;1HA🍺B\\033[7;1HA  B"
        body += "\\033[8;48H界B\\033[10;48HB\\033[16;1HAB\\033[12;1HWIDE_READY"
        command = f"printf '{body}'; read -r glyph_hold; printf '\\033[13;1HCURSOR_READY\\033[2;2H\\033[2 q\\033[?25h'; read -r glyph_hold; printf '\\033[14;1HTRAILING_READY\\033[5;3H'; read -r glyph_hold\n"
        ctl("pane", "input", "--pane", pane, "--text", command)
        wait(lambda s: "WIDE_READY" in ctl("pane", "content", "--pane", pane)["text"], "glyph matrix output")
        path = directory / f"glyphs-{size}.png"
        ctl("ui", "screenshot", "--output", str(path))
        image = Image.open(path).convert("RGB")
        grid = ctl("ui", "snapshot")["terminal_grids"][0]
        gx, gy = grid["rect"][:2]
        cw, lh = grid["cell_width"], grid["cell_height"]
        crops = []
        for row, column in ((1,1),(2,1),(3,1),(6,1),(6,3),(8,0)):
            crop = image.crop((gx+column*cw, gy+row*lh, gx+(column+2)*cw, gy+(row+1)*lh))
            mask = crop.convert("L").point(lambda p: 255 if p > 10 else 0)
            ink = mask.getbbox()
            assert ink and ink[0] >= 1 and ink[2] <= 2*cw-1 and ink[1] >= 1 and ink[3] <= lh-1, (size,row,column,ink)
            assert abs(ink[0]-(2*cw-ink[2])) <= 2 and abs(ink[1]-(lh-ink[3])) <= 2, ("uncentered",size,row,column,ink)
            crops.append(crop)
        # Each following B must begin in the cell after the two-column icon.
        for row, column in ((1,3),(6,5),(8,2)):
            crop = image.crop((gx+column*cw, gy+row*lh, gx+(column+1)*cw, gy+(row+1)*lh))
            assert crop.convert("L").getbbox(), ("neighbor overwritten",size,row,column)
        for row, column in ((9,47),(15,1)):
            crop = image.crop((gx+column*cw,gy+row*lh,gx+(column+1)*cw,gy+(row+1)*lh))
            ink = crop.convert("L").point(lambda p:255 if p>10 else 0).getbbox()
            assert ink and ink[0]>=1 and ink[2]<=cw-1, ("narrow icon clipped",size,row,ink)
        assert image.crop((gx+2*cw,gy+15*lh,gx+3*cw,gy+16*lh)).convert("L").getbbox(), "icon covered adjacent B"
        # Select only a glyph's leading/trailing half through the real mouse
        # path. Its two visual cells must receive the same highlight.
        for row in (1,4,5):
            for start,end in ((0,1),(3,2),(1,0),(2,3)):
                y = gy+(row+.5)*lh
                result = ctl("ui","drag","--x",str(gx+(start+.5)*cw),"--y",str(y),"--to-x",str(gx+(end+.5)*cw),"--to-y",str(y))
                assert result["handled"], "terminal selection drag not handled"
                selected_path = directory / f"selection-{size}-{row}-{start}-{end}.png"
                ctl("ui","screenshot","--output",str(selected_path))
                selected = Image.open(selected_path).convert("RGB")
                colors = [selected.getpixel((gx+column*cw,gy+row*lh)) for column in (1,2)]
                assert colors[0]==colors[1] and colors[0]!=image.getpixel((gx+cw,gy+row*lh)), ("half glyph selected",size,row,start,end,colors)
                outside = 3 if start in (0,1) else 0
                point = (gx+outside*cw,gy+row*lh)
                assert selected.getpixel(point)==image.getpixel(point), ("selection over-expanded",size,row,start,end)
        ctl("ui","click","--x",str(gx+40*cw),"--y",str(gy+17*lh+lh/2))
        ctl("pane", "input", "--pane", pane, "--text", "\n")
        wait(lambda s: "CURSOR_READY" in ctl("pane", "content", "--pane", pane)["text"], "cursor output applied")
        cursor = directory / f"cursor-{size}.png"
        def cursor_ready(state):
            ctl("ui", "screenshot", "--output", str(cursor))
            inverted = Image.open(cursor).convert("RGB").crop((gx+cw, gy+lh, gx+3*cw, gy+2*lh))
            return all(min(inverted.getpixel(point)) >= 200 for point in ((0,0),(2*cw-1,0),(0,lh-1),(2*cw-1,lh-1)))
        wait(cursor_ready, "two-column block cursor")
        inverted = Image.open(cursor).convert("L").crop((gx+cw, gy+lh, gx+3*cw, gy+2*lh))
        threshold = (inverted.getpixel((0,0)) + inverted.getextrema()[0]) / 2
        cursor_ink = inverted.point(lambda p: 255 if p < threshold else 0).getbbox()
        original_ink = crops[0].convert("L").point(lambda p: 255 if p > 127 else 0).getbbox()
        assert cursor_ink and original_ink and all(abs(a-b)<=1 for a,b in zip(cursor_ink,original_ink)), ("cursor clipped or displaced icon",size,cursor_ink,original_ink)
        ctl("pane", "input", "--pane", pane, "--text", "\n")
        wait(lambda s: "TRAILING_READY" in ctl("pane", "content", "--pane", pane)["text"], "trailing cursor output applied")
        trailing = directory / f"cursor-trailing-{size}.png"
        def trailing_ready(state):
            ctl("ui", "screenshot", "--output", str(trailing))
            crop = Image.open(trailing).convert("L").crop((gx+cw,gy+4*lh,gx+3*cw,gy+5*lh))
            return all(crop.getpixel(point)>=200 for point in ((0,0),(2*cw-1,0),(0,lh-1),(2*cw-1,lh-1)))
        wait(trailing_ready, "cursor on trailing half preserves true wide character")
        crop = Image.open(trailing).convert("L").crop((gx+cw,gy+4*lh,gx+3*cw,gy+5*lh))
        threshold = (crop.getpixel((0,0))+crop.getextrema()[0])/2
        ink = crop.point(lambda p:255 if p<threshold else 0).getbbox()
        original = image.crop((gx+cw,gy+4*lh,gx+3*cw,gy+5*lh)).convert("L").point(lambda p:255 if p>127 else 0).getbbox()
        assert ink and original and all(abs(a-b)<=1 for a,b in zip(ink,original)), ("wide cursor clipped",size,ink,original)
        sheet = Image.new("RGB", (2*cw*len(crops),lh), "black")
        for index,crop in enumerate(crops):
            sheet.paste(crop,(2*cw*index,0))
        sheet.resize((sheet.width*4,sheet.height*4)).save(directory / f"icon-details-{size}.png")
        ctl("pane", "input", "--pane", pane, "--text", "\n")
        for index,prompt in enumerate((" "*20+"> ", "X"*20+"> ")):
            ctl("pane", "input", "--pane", pane, "--text", f"PROMPT='{prompt}'; RPROMPT=''; printf '\\033[0m\\033[2J\\033[H'\n")
            wait(lambda s: any(prompt.rstrip() in line for line in ctl("pane","content","--pane",pane)["lines"]), "zsh prompt stays on one physical row")
            ctl("ui", "key", "text:#OK")
            wait(lambda s: any(prompt+"#OK" in line for line in ctl("pane","content","--pane",pane)["lines"]), "ZLE input stays on same row as prompt")
            ctl("ui", "screenshot", "--output", str(directory / f"zsh-prompt-{size}-{index}.png"))
            ctl("ui", "key", "ctrl-u")
            wait(lambda s: all("#OK" not in line for line in ctl("pane","content","--pane",pane)["lines"]), "ZLE line clearing")
        print(f"PASS font {size}: full-glyph selection in both directions, icon fitting, styles, adjacency, wrapping, cursor and one-line zsh prompt; cell={cw}x{lh}", flush=True)
    finally:
        try:
            ctl("server", "shutdown")
        except Exception:
            pass
        if gui.poll() is None:
            gui.terminate()
        try:
            gui.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gui.kill()
            gui.wait(timeout=5)
        log.close()

print(f"PASS native wide glyph verification; artifacts={directory}", flush=True)
