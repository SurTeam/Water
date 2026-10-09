#!/usr/bin/env python3
"""Real GUI checks for selection during output, Delete, icat and image paste."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time

root = Path(__file__).resolve().parent.parent
water = os.environ.get("WATER_BIN", str(root/"target/go-ui-smoke/water"))
directory = Path(tempfile.mkdtemp(prefix="water-graphics.", dir="/tmp"))
socket = str(directory/"water.sock")
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
image = Path(os.environ.get("WATER_TEST_IMAGE", str(Path.home()/"Pictures/20150803201548_uVt5k.jpeg")))
assert image.is_file(), image
zdir = directory/"zsh"
zdir.mkdir()
(zdir/".zshrc").write_text("PS1='WATER_READY> '\n")
config = directory/"config.json"
config.write_text(json.dumps({"startup":{"window_columns":90,"window_rows":26,"default_cwd":str(directory)},"shell":{"program":shell,"args":["-i"]},"server":{"detached":False}}))
log = (directory/"gui.log").open("w")
gui = subprocess.Popen([water,"--control-socket",socket,"--config",str(config)],env=dict(os.environ,ZDOTDIR=str(zdir)),stdout=log,stderr=subprocess.STDOUT)
print(f"owned_gui_pid={gui.pid} socket={socket}",flush=True)

def ctl(*args):
    result = subprocess.run([water,"ctl","--socket",socket,*map(str,args)],capture_output=True,timeout=6)
    if result.returncode: raise RuntimeError(result.stderr.decode(errors="replace"))
    return json.loads(result.stdout) if result.stdout.strip() else None

def grid(state):
    return next(g for g in state["terminal_grids"] if str(g["pane_id"])==str(state["frame_focused_pane"]))

def wait(predicate,description,timeout=8):
    deadline=time.monotonic()+timeout
    state={}
    while time.monotonic()<deadline:
        if gui.poll() is not None: raise RuntimeError("GUI exited")
        try:
            state=ctl("ui","snapshot")
            if predicate(state): return state
        except (RuntimeError,subprocess.SubprocessError): pass
        time.sleep(.02)
    (directory/"failure.json").write_text(json.dumps(state))
    if "pane" in globals(): (directory/"failure-content.json").write_text(json.dumps(ctl("pane","content","--pane",pane)))
    raise RuntimeError("Timeout "+description+"; artifacts="+str(directory))

def command(text):
    ctl("pane","input","--pane",pane,"--text",text+"\n")

def content(): return ctl("pane","content","--pane",pane)["text"]

saved=False
try:
    state=wait(lambda s:s.get("terminal_grids"),"GUI grid")
    pane=str(state["frame_focused_pane"])
    print(f"owned_server_pid={ctl('server','info')['server_pid']}",flush=True)
    wait(lambda s:"WATER_READY>" in content(),"zsh prompt")
    ctl("ui","key","text:printf 'DELETE_RESULT:%s\\n' abXc")
    ctl("ui","key","Left")
    ctl("ui","key","Left")
    ctl("ui","key","Delete")
    ctl("ui","key","Return")
    wait(lambda s:"DELETE_RESULT:abc" in content() or "DELETE_RESULT:abX" in content(),"delete key in zsh")
    print("PASS Delete key handled in zsh",flush=True)

    command("for i in {1..70}; do print HISTORY_$i; done; print HISTORY_READY")
    state=wait(lambda s:"HISTORY_READY" in content(),"history fixture")
    g=grid(state)
    x=g["rect"][0]+3*g["cell_width"]; y=g["rect"][1]+3*g["cell_height"]
    ctl("ui","wheel","--x",x,"--y",y,"--dy",-20)
    state=wait(lambda s:grid(s)["y_disp"]<grid(s)["y_base"],"history scroll")
    ctl("ui","drag","--x",x,"--y",y,"--to-x",x+5*g["cell_width"],"--to-y",y)
    state=wait(lambda s:grid(s)["selection"]["Active"],"history selection")
    selected=grid(state)["selection"]; position=grid(state)["y_disp"]
    # Sending through pane.input doesn't count as a GUI edit or clear selection.
    command("for i in {1..40}; do print LIVE_$i; done")
    wait(lambda s:grid(s)["y_base"]>g["y_base"],"new output")
    state=ctl("ui","snapshot")
    assert grid(state)["selection"]==selected and grid(state)["y_disp"]==position
    ctl("ui","screenshot","--output",directory/"selection-output.png")
    print("PASS selection and history position survive output",flush=True)

    ctl("ui","key","text: ")
    ctl("ui","key","ctrl-u")
    for mode,options in (("normal",""),("unicode","--unicode-placeholder --image-id=33554474")):
        command(f"kitten icat --stdin=no --detection-timeout=3 {options} {shlex.quote(str(image))}; print ICAT_{mode}_DONE")
        wait(lambda s:f"ICAT_{mode}_DONE" in content(),"icat "+mode,12)
        state=wait(lambda s:bool(grid(s)["images"]),"rendered "+mode+" image")
        imgs=grid(state)["images"]
        assert max(i["height"] for i in imgs)>1
        ctl("ui","screenshot","--output",directory/f"icat-{mode}.png")
        command("for i in {1..120}; do print AFTER_IMAGE_$i; done; print SCROLLED_DONE")
        wait(lambda s:"SCROLLED_DONE" in content(),"image scrolled away")
        g=grid(ctl("ui","snapshot"))
        ctl("ui","wheel","--x",x,"--y",y,"--dy",-4800)
        state=wait(lambda s:bool(grid(s)["images"]),"image returns from history")
        ctl("ui","screenshot","--output",directory/f"icat-{mode}-history.png")
        print(f"PASS icat {mode}: image height={max(i['height'] for i in imgs)}, restored in history",flush=True)
        ctl("ui","key","text: ")
        ctl("ui","key","ctrl-u")

    fixture=root/"scripts/clipboard-fixture.swift"
    subprocess.run(["swift",str(fixture),"save",str(directory/"clipboard.json")],check=True,timeout=20)
    saved=True
    subprocess.run(["swift",str(fixture),"image",str(image)],check=True,timeout=20)
    # A raw PTY receiver verifies exactly the event coding agents need, with
    # no bracketed text wrapper or PNG bytes pasted as text.
    command(shlex.join([str(Path.home()/".venv/bin/python"),"-c","import os,tty,termios; f=0; old=termios.tcgetattr(f); tty.setraw(f); print('PASTE_READY',flush=True); b=os.read(f,1); termios.tcsetattr(f,termios.TCSANOW,old); print('PASTE_BYTE:'+b.hex(),flush=True)"]))
    wait(lambda s:"PASTE_READY" in content(),"paste receiver")
    ctl("ui","key","cmd-v")
    wait(lambda s:"PASTE_BYTE:16" in content(),"native image paste")
    print("PASS image clipboard paste delivers Ctrl+V to the agent PTY",flush=True)

    if agent:=os.environ.get("WATER_TEST_AGENT"):
        command(agent)
        state=wait(lambda s:any(name in content() for name in ("OpenAI Codex", "Claude Code", "opencode")),"agent startup",15)
        if "Yes, I trust this folder" in content():
            ctl("ui","key","Down")
            ctl("ui","key","Return")
        wait(lambda s:any(name in content() for name in ("Ask Codex", "for shortcuts", "bypass permissions", "What can I help", "Welcome back", "manual mode on")),"agent composer",15)
        ctl("ui","screenshot","--output",directory/"agent-before-paste.png")
        ctl("ui","key","cmd-v")
        state=wait(lambda s:any(label in content() for label in ("[Image #1]", "[Image 1]", "[image", "Image #1", "image.png", "image/jpeg", "image/png")),"agent image attachment",12)
        ctl("ui","screenshot","--output",directory/"agent-image-paste.png")
        print("PASS agent image attachment: "+agent,flush=True)
finally:
    if saved: subprocess.run(["swift",str(root/"scripts/clipboard-fixture.swift"),"restore",str(directory/"clipboard.json")],check=True,timeout=20)
    try: ctl("server","shutdown")
    except Exception: pass
    if gui.poll() is None: gui.terminate()
    try: gui.wait(timeout=5)
    except subprocess.TimeoutExpired: gui.kill();gui.wait(timeout=5)
    log.close()
    print(f"artifacts={directory}",flush=True)
