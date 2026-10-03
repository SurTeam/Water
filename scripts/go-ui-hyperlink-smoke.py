#!/usr/bin/env python3
"""Verify displayed links against the actual native click opener argument."""
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import time
from urllib.parse import unquote, urlparse

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root / "target/go-app/dev/water"))
directory = Path(tempfile.mkdtemp(prefix="water-hyperlink.", dir="/tmp"))
socket = str(directory / "water.sock")
opened = directory / "opened.jsonl"
opener = directory / "open"
opener.write_text("#!"+sys.executable+"\nimport json,os,sys\nwith open(os.environ['WATER_TEST_OPEN_LOG'],'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n")
opener.chmod(0o700)
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config = directory / "config.json"
config.write_text(json.dumps({"startup": {"window_columns": 100, "window_rows": 40},
    "terminal": {"font_size": 16, "hyperlinks": True, "hyperlink_command_click": False},
    "shell": {"program": shell, "args": ["-il"]}, "server": {"detached": False}}))
env = dict(os.environ, PATH=str(directory)+os.pathsep+os.environ["PATH"], WATER_TEST_OPEN_LOG=str(opened))
log = (directory / "gui.log").open("w")
gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], env=env, stdout=log, stderr=subprocess.STDOUT)
print(f"owned_gui_pid={gui.pid} socket={socket}", flush=True)

def ctl(*args):
    result = subprocess.run([water,"ctl","--socket",socket,*args], capture_output=True, timeout=6)
    if result.returncode: raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout) if args != ("server","shutdown") else None

def wait(predicate, description):
    deadline = time.monotonic()+10
    while time.monotonic()<deadline:
        if gui.poll() is not None: raise RuntimeError("GUI exited; "+str(directory))
        try:
            result = predicate()
            if result: return result
        except (RuntimeError, subprocess.SubprocessError): pass
        time.sleep(.02)
    raise RuntimeError("Timeout "+description+"; artifacts="+str(directory))

def targets():
    return [json.loads(line)[0] for line in opened.read_text().splitlines()] if opened.exists() else []

def click(row, column, expected):
    grid = ctl("ui","snapshot")["terminal_grids"][0]
    gx,gy = grid["rect"][:2]
    before = len(targets())
    ctl("ui","click","--x",str(gx+(column+.5)*grid["cell_width"]),"--y",str(gy+(row+.5)*grid["cell_height"]))
    wait(lambda:len(targets())>before,"native opener")
    assert targets()[before] == expected, ("wrong target",row,column)

try:
    state = wait(lambda:ctl("ui","snapshot").get("terminal_grids"),"terminal grid")
    pane = str(ctl("ui","snapshot")["frame_focused_pane"])
    print(f"owned_server_pid={ctl('server','info')['server_pid']}",flush=True)
    # Deliberately erase and reuse an OSC8 id before creating neighboring links.
    osc = lambda uri,label,tag="": "\\033]8;"+tag+";"+uri+"\\033\\\\"+label+"\\033]8;;\\033\\\\"
    body = "\\033[2J\\033[H"+osc("file:///tmp/water-first","old","id=tag")+"\\r\\033[2K"
    body += osc("file:///tmp/water-first","FIRST","id=tag")+"\\r\\n"
    for uri,label in (("file:///tmp/water-second","SECOND"),("file:///tmp/water-cjk","界"),("file:///tmp/water-icon"," "),("file:///tmp/water-last","LAST")):
        body += osc(uri,label)+"\\r\\n"
    body += "WATER_LINK_READY"
    ctl("pane","input","--pane",pane,"--text","printf "+shlex.quote(body)+"; read -r link_hold\n")
    wait(lambda:"WATER_LINK_READY" in ctl("pane","content","--pane",pane)["text"],"link matrix")
    for row,col,path in ((0,0,"first"),(1,0,"second"),(2,0,"cjk"),(2,1,"cjk"),(3,0,"icon"),(3,1,"icon"),(4,0,"last")):
        click(row,col,"/tmp/water-"+path)
    ctl("ui","screenshot","--output",str(directory/"links.png"))
    print("PASS: reused links, neighboring rows and both halves of wide glyphs open the displayed target",flush=True)
    ctl("pane","input","--pane",pane,"--text","\n")
    # Read actual Documents link metadata without printing personal filenames.
    raw = subprocess.run([shell,"-ilc","ls --color=never --hyperlink=always -1 ~/Documents"],capture_output=True,timeout=8).stdout.decode()
    entries = re.findall(r"\x1b\]8;[^;]*;([^\x1b\x07]+)(?:\x1b\\|\x07)([^\x1b]+)\x1b\]8;;(?:\x1b\\|\x07)",raw)
    ctl("pane","input","--pane",pane,"--text","printf '\\033[2J\\033[H'; ls ~/Documents; printf '\\nDOCUMENTS_LINK_READY\\n'; read -r link_hold\n")
    wait(lambda:"DOCUMENTS_LINK_READY" in ctl("pane","content","--pane",pane)["text"],"actual ls Documents")
    lines = ctl("pane","content","--pane",pane)["lines"]
    rows = []
    for row,line in enumerate(lines):
        matches = [(line.find(label),uri) for uri,label in entries if label and label in line]
        if matches: rows.append((row,*min(matches)))
    assert len(rows)>=2, "need two visible Documents link rows"
    for row,column,uri in rows[-2:]: click(row,column,unquote(urlparse(uri).path))
    ctl("ui","screenshot","--output",str(directory/"documents.png"))
    print("PASS: actual ls ~/Documents penultimate and last displayed rows open their respective paths",flush=True)
finally:
    try: ctl("server","shutdown")
    except Exception: pass
    if gui.poll() is None: gui.terminate()
    try: gui.wait(timeout=5)
    except subprocess.TimeoutExpired: gui.kill(); gui.wait(timeout=5)
    log.close()
    print(f"artifacts={directory}",flush=True)
