#!/usr/bin/env python3
"""Verify titlebar tabs and the real Codex composer in an owned native GUI."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
from PIL import Image

os.environ["WATER_TEST_UNFOCUSED"] = "1"

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN",str(root/"target/go-app/dev/water"))
directory = Path(tempfile.mkdtemp(prefix="water-tabs-colors.",dir="/tmp"))
socket = str(directory/"water.sock")
shell = next(p for p in ("/opt/homebrew/bin/zsh","/bin/zsh") if Path(p).exists())
config = directory/"config.json"
config.write_text(json.dumps({"startup":{"window_columns":100,"window_rows":32},
    "shell":{"program":shell,"args":["-f"]},"server":{"detached":False},
    "theme":{"terminal_background":"#123456","terminal_foreground":"#eeeeee"}}))
log = (directory/"gui.log").open("w")
env = dict(os.environ)
# Coding-tool shells can disable colors globally; use a normal GUI environment.
env.pop("NO_COLOR",None)
gui = subprocess.Popen([water,"--control-socket",socket,"--config",str(config)],env=env,stdout=log,stderr=subprocess.STDOUT)
print(f"owned_gui_pid={gui.pid} socket={socket}",flush=True)

def ctl(*args):
    result = subprocess.run([water,"ctl","--socket",socket,*args],capture_output=True,timeout=6)
    if result.returncode: raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout) if args!=("server","shutdown") else None

def wait(predicate,description):
    deadline = time.monotonic()+12
    while time.monotonic()<deadline:
        if gui.poll() is not None: raise RuntimeError("GUI exited; "+str(directory))
        try:
            state = ctl("ui","snapshot")
            if predicate(state): return state
        except (RuntimeError,subprocess.SubprocessError): pass
        time.sleep(.02)
    ctl("ui","screenshot","--output",str(directory/"failure.png"))
    if "pane" in globals():
        (directory/"failure-content.json").write_text(json.dumps(ctl("pane","content","--pane",pane)))
    raise RuntimeError("Timeout "+description+"; artifacts="+str(directory))

def tabs(state): return [h for h in state["automation_hits"] if h["kind"]=="tab"]

try:
    state = wait(lambda s:s.get("terminal_grids"),"GUI grid")
    print(f"owned_server_pid={ctl('server','info')['server_pid']}",flush=True)
    pane = str(state["frame_focused_pane"])
    dump = ctl("state")
    workspace = next(w for w in dump["workspaces"] if w["id"]==dump["active_workspace"])
    first = workspace["tabs"][0]["id"]
    ctl("tab","rename",first,"x")
    wait(lambda s:tabs(s)[0]["label"]=="x","short title")
    short_width = tabs(ctl("ui","snapshot"))[0]["rect"]
    ctl("tab","rename",first,"longer title example")
    state = wait(lambda s:tabs(s)[0]["label"]=="longer title example","long title")
    long_width = tabs(state)[0]["rect"]
    assert long_width[2]-long_width[0]>short_width[2]-short_width[0]+40,"tab width did not follow text"
    height = state["titlebar_height"]
    ctl("ui","key","cmd-e")
    state = wait(lambda s:not s["sidebar_visible"] and tabs(s),"hide sidebar")
    assert state["titlebar_height"]==height,"titlebar grew a second row"
    assert all(h["rect"][3]<=height for h in tabs(state)),"tab outside titlebar"
    controls = [h for h in state["automation_hits"] if h["kind"] in ("window_close","window_minimize","window_maximize")]
    assert len(controls)==3,"missing titlebar control hit regions"
    assert all(tabs(state)[0]["rect"][0]>=h["rect"][2] for h in controls),"tab overlaps controls"
    ctl("ui","screenshot","--output",str(directory/"hidden-sidebar.png"))
    print("PASS: title-sized tabs stay in one titlebar row after hiding sidebar",flush=True)
    # Do not submit a prompt: only launch Codex and inspect its idle composer.
    command = "cd "+shlex.quote(str(root))+"; "+shlex.quote(str(Path.home()/".local/bin/codex"))+" --no-alt-screen\n"
    ctl("pane","input","--pane",pane,"--text",command)
    def composer(state):
        content = ctl("pane","content","--pane",pane)
        return "Ask Codex to do anything" in content["text"] or "context left" in content["text"] or "for shortcuts" in content["text"]
    state = wait(composer,"Codex idle composer")
    path = directory/"codex-composer.png"
    def background_visible(state):
        ctl("ui","screenshot","--output",str(path))
        image = Image.open(path).convert("RGB")
        grid = state["terminal_grids"][0]
        gx,gy = grid["rect"][:2]
        cw,lh = grid["cell_width"],grid["cell_height"]
        # Codex blends white at 12% over the queried #123456 background.
        expected = tuple(round(c*.88+255*.12) for c in (18,52,86))
        rows = []
        for row in range(grid["rows"]):
            pixels = [image.getpixel((int(gx+col*cw),int(gy+(row+.5)*lh))) for col in range(3,grid["columns"]-3)]
            matching = sum(all(abs(a-b)<=2 for a,b in zip(pixel,expected)) for pixel in pixels)
            if matching>len(pixels)*.8: rows.append(row)
        return bool(rows)
    wait(background_visible,"actual Codex composer background pixels")
    print("PASS: real Codex input background derives from the configured terminal RGB",flush=True)
finally:
    try: ctl("server","shutdown")
    except Exception: pass
    if gui.poll() is None: gui.terminate()
    try: gui.wait(timeout=5)
    except subprocess.TimeoutExpired: gui.kill();gui.wait(timeout=5)
    log.close()
    print(f"artifacts={directory}",flush=True)
