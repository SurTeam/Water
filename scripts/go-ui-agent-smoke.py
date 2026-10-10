#!/usr/bin/env python3
"""Verify foreground agent discovery, both sidebar modes and settings via ctl."""
import json
import os
from pathlib import Path
import subprocess
import shlex
import tempfile
import time

os.environ["WATER_TEST_UNFOCUSED"] = "1"

root = Path(__file__).resolve().parent.parent
water = str(root / "target/go-ui-smoke/water")
directory = Path(tempfile.mkdtemp(prefix="water-agents.", dir="/tmp"))
socket = str(directory / "water.sock")
config = directory / "config.json"
shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"startup":{"default_cwd":str(directory)},"shell": {"program": shell, "args": ["-f"]}, "server": {"detached": False, "detach_on_quit": False}}))
log = (directory / "gui.log").open("w")
gui = subprocess.Popen([water, "--control-socket", socket, "--config", str(config)], stdout=log, stderr=subprocess.STDOUT)

def ctl(*args):
    p = subprocess.run([water, "ctl", "--socket", socket, *map(str,args)], capture_output=True, timeout=5)
    if p.returncode: raise RuntimeError(p.stderr.decode())
    return json.loads(p.stdout)

def wait(predicate, description):
    deadline = time.monotonic()+8
    while time.monotonic()<deadline:
        if gui.poll() is not None: raise RuntimeError((directory/"gui.log").read_text())
        try:
            value = predicate()
            if value: return value
        except (RuntimeError, subprocess.TimeoutExpired): pass
        time.sleep(.025)
    raise RuntimeError("Timeout: "+description)

def click(label=None, kind=None, target=None):
    def locate():
        s = ctl("ui","snapshot")
        hits = [h for h in s.get("automation_hits",[]) if (label is None or h.get("label")==label) and (kind is None or h["kind"]==kind) and (target is None or h.get("id")==target) and h["rect"][3]>h["rect"][1]]
        if hits: return hits[-1]
        if s.get("settings_visible"):
            w,h=s["frame_size"]
            ctl("ui","wheel","--x",w*.65,"--y",h*.5,"--dy",100)
    h=wait(locate,"hit "+str(label or kind))
    x0,y0,x1,y1=h["rect"]
    ctl("ui","click","--x",(x0+x1)/2,"--y",(y0+y1)/2)

try:
    initial=wait(lambda: ctl("ui","snapshot").get("frame_focused_pane"),"GUI ready")
    pane=initial
    # Start the installed CLIs without submitting any prompts or model tasks.
    resolved=subprocess.run([shell,"-c",'eval "$(fnm env --shell zsh)"\ncommand -v claude pi codex opencode'],text=True,capture_output=True,timeout=5,check=True).stdout.splitlines()
    assert len(resolved)==4,resolved
    for path,kind in zip(resolved,("claude_code","pi","codex","opencode")):
        ctl("pane","input","--pane",pane,"--text",'eval "$(fnm env --shell zsh)"\nexec '+shlex.quote(path)+'\n')
        wait(lambda: [a for a in ctl("state").get("agents",[]) if a["kind"]==kind],"installed "+kind+" classified")
        ctl("ui","screenshot","--output",str(directory/(kind+"-startup.png")))
        ctl("terminal","spawn","--pane",pane,"--program",shell,"--","-f")
        wait(lambda: not ctl("state").get("agents"),kind+" cleared after replacement")
    # Harmless foreground probes: real PTY jobs with the four exact agent names.
    for name,kind in (("claude","claude_code"),("pi","pi"),("codex","codex"),("opencode","opencode")):
        ctl("pane","input","--pane",pane,"--text",f"printf '\\033]2;{name} session\\007'; exec -a {name} /bin/sleep 30\n")
        agents=wait(lambda: [a for a in ctl("state").get("agents",[]) if a["kind"]==kind],name+" detected")
        assert agents[0]["pane_id"]==pane
        wait(lambda: any(a["pane_id"]==pane and a["title"]==name+" session" and a["title_source"]=="osc" and a["status"]=="Running" for a in ctl("ui","snapshot")["sidebar_agents"]), "workspace OSC title and visible running state")
        ctl("ui","screenshot","--output",str(directory/(name+"-workspace-status.png")))
        click(kind="agent")
        assert ctl("ui","snapshot")["frame_focused_pane"]==pane
        # Replace the owned probe with a real configured shell through the command path.
        ctl("terminal","spawn","--pane",pane,"--program",shell,"--","-f")
        wait(lambda: not ctl("state").get("agents"),name+" stopped")
    ctl("ui","key","cmd-,")
    ctl("ui","click","--x",5,"--y",100)
    assert ctl("ui","snapshot")["settings_visible"]
    click(label="category:UI")
    click(label="choice:SidebarAgentMode:split")
    click(label="cancel")
    assert ctl("ui","snapshot")["settings_visible"]
    assert "Unsaved changes" in ctl("ui","snapshot")["settings_message"]
    click(label="cancel")
    wait(lambda: not ctl("ui","snapshot")["settings_visible"],"discard")
    ctl("ui","key","cmd-,")
    click(label="category:UI")
    click(label="choice:SidebarAgentMode:split")
    click(label="field:UI.SidebarAgentRowWidth")
    ctl("ui","key","cmd-a")
    ctl("ui","key","text:0.3")
    click(label="save")
    wait(lambda: ctl("ui","snapshot").get("settings_message")=="Settings saved","save split mode")
    assert ctl("ui","snapshot")["settings_visible"]
    ctl("ui","key","cmd-,")
    wait(lambda: not ctl("ui","snapshot")["settings_visible"],"close saved settings")
    targets=[]
    ctl("workspace","rename","--title","Project A")
    for i,name in enumerate(("claude","pi","codex","opencode")):
        previous=ctl("ui","snapshot")["frame_focused_pane"]
        if i==2:
            ctl("workspace","new")
            ctl("workspace","rename","--title","Project B")
        else:
            ctl("tab","new")
        target=wait(lambda: (lambda p: p if p and p!=previous else None)(ctl("ui","snapshot")["frame_focused_pane"]),"new pane")
        ctl("pane","input","--pane",target,"--text",f"printf '\\033]0;{name} task\\033\\\\'; exec -a {name} /bin/sleep 60\n")
        targets.append(target)
    wait(lambda: len(ctl("state").get("agents",[]))==4,"four concurrent agents")
    shelf=wait(lambda: (lambda s: s if len(s["sidebar_agents"])==4 and all(a["title_source"]=="osc" and a["status"]=="Running" for a in s["sidebar_agents"]) else None)(ctl("ui","snapshot")), "inactive OSC titles on shelf")
    assert {a["workspace_name"] for a in shelf["sidebar_agents"]} == {"Project A","Project B"}
    footer=next(h for h in shelf["automation_hits"] if h["kind"]=="new_workspace")
    assert all(h["rect"][2]-h["rect"][0]==footer["rect"][2]-footer["rect"][0] for h in shelf["automation_hits"] if h["kind"]=="agent"), "shelf rows must ignore configured row-width scaling"
    for target in targets:
        click(kind="agent",target=target)
        wait(lambda: ctl("ui","snapshot")["frame_focused_pane"]==target,"switch to exact agent pane")
    ctl("ui","screenshot","--output",str(directory/"split-agents.png"))
    assert ctl("ui","snapshot")["effective_config"]["ui"]["sidebar_agent_mode"]=="split"
    assert json.loads(config.read_text())["ui"]["sidebar_agent_mode"]=="split"
    ctl("workspace","rename","--title","Project B renamed")
    wait(lambda: {a["workspace_name"] for a in ctl("ui","snapshot")["sidebar_agents"]}=={"Project A","Project B renamed"}, "shelf follows workspace rename")
    ctl("pane","input","--pane",targets[-1],"--text","\u0003")
    wait(lambda: not any(a["pane_id"]==targets[-1] for a in ctl("ui","snapshot")["sidebar_agents"]), "exited pane auto-closes without stale running entry")
    ctl("ui","screenshot","--output",str(directory/"exited-agent.png"))
    print("PASS four installed CLI startups, foreground probes, four concurrent agents with exact pane switching, split shelf, modal settings and dirty discard; artifacts="+str(directory),flush=True)
finally:
    try: ctl("server","shutdown")
    except Exception: pass
    if gui.poll() is None: gui.terminate()
    try: gui.wait(timeout=5)
    except subprocess.TimeoutExpired: gui.kill(); gui.wait()
    log.close()
