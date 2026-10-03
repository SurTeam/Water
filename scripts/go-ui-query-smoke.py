#!/usr/bin/env python3
"""Check OSC/CSI/DCS replies through an owned native GUI and real PTY."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN",str(root/"target/go-app/dev/water"))
directory = Path(tempfile.mkdtemp(prefix="water-queries.",dir="/tmp"))
socket = str(directory/"water.sock")
shell = next(p for p in ("/opt/homebrew/bin/zsh","/bin/zsh") if Path(p).exists())
config = directory/"config.json"
config.write_text(json.dumps({"startup":{"window_columns":100,"window_rows":32},
    "shell":{"program":shell,"args":["-f"]},"server":{"detached":False}}))
log = (directory/"gui.log").open("w")
gui = subprocess.Popen([water,"--control-socket",socket,"--config",str(config)],stdout=log,stderr=subprocess.STDOUT)
print(f"owned_gui_pid={gui.pid} socket={socket}",flush=True)
previous_clipboard = subprocess.run(["pbpaste"],capture_output=True,timeout=3).stdout

def ctl(*args):
    result = subprocess.run([water,"ctl","--socket",socket,*args],capture_output=True,timeout=6)
    if result.returncode: raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout) if args!=("server","shutdown") else None

def wait(predicate,description):
    deadline = time.monotonic()+10
    while time.monotonic()<deadline:
        if gui.poll() is not None: raise RuntimeError("GUI exited; "+str(directory))
        try:
            state = ctl("ui","snapshot")
            if predicate(state): return state
        except (RuntimeError,subprocess.SubprocessError): pass
        time.sleep(.02)
    (directory/"failure.json").write_text(json.dumps(state))
    if "pane" in globals(): (directory/"failure-content.json").write_text(json.dumps(ctl("pane","content","--pane",pane)))
    raise RuntimeError("Timeout "+description+"; artifacts="+str(directory))

try:
    state = wait(lambda s:s.get("terminal_grids"),"GUI grid")
    print(f"owned_server_pid={ctl('server','info')['server_pid']}",flush=True)
    for phase in range(2):
        pane = str(state["frame_focused_pane"])
        grid = next(g for g in state["terminal_grids"] if str(g["pane_id"])==pane)
        expected = dict(grid,screen_size_pixels=state["screen_size_pixels"],frame_size=state["frame_size"])
        scale = state["display_scale"]
        expected["window_position_pixels"] = [int(p*scale+.5) for p in state["window_position"]]
        expected["text_position_pixels"] = [a+b for a,b in zip(expected["window_position_pixels"],grid["rect"][:2])]
        command = shlex.join([str(Path.home()/".venv/bin/python"),str(root/"scripts/terminal-query-probe.py"),json.dumps(expected)])+"\n"
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
    subprocess.run(["pbcopy"],input=previous_clipboard,timeout=3,check=True)
    try: ctl("server","shutdown")
    except Exception: pass
    if gui.poll() is None: gui.terminate()
    try: gui.wait(timeout=5)
    except subprocess.TimeoutExpired: gui.kill();gui.wait(timeout=5)
    log.close()
    print(f"artifacts={directory}",flush=True)
