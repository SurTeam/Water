#!/usr/bin/env python3
"""Exercise the real macOS notification authorization/delivery path via BEL."""
import json
import plistlib
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

root=Path(__file__).resolve().parent.parent
directory=Path(tempfile.mkdtemp(prefix="water-notify.",dir="/tmp"))
app=directory/"Water Notification Test.app"
executables=app/"Contents/MacOS"
executables.mkdir(parents=True)
for source,name in (("water","water-dev"),("water-server","water-srv-dev")):
    shutil.copy2(root/"target/go-ui-smoke"/source,executables/name)
(app/"Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier":"dev.water.terminal.dev.notification-test","CFBundleExecutable":"water-dev","CFBundleName":"Water Notification Test","CFBundlePackageType":"APPL","NSHighResolutionCapable":True}))
# Ad-hoc signatures are only for this owned test bundle, never a release artifact.
subprocess.run(["codesign","--force","--sign","-",str(executables/"water-srv-dev")],capture_output=True,check=True)
subprocess.run(["codesign","--force","--sign","-",str(app)],capture_output=True,check=True)
water=str(executables/"water-dev")
socket=str(directory/"water.sock")
config=directory/"config.json"
shell=next(p for p in ("/opt/homebrew/bin/zsh","/bin/zsh") if Path(p).exists())
config.write_text(json.dumps({"startup":{"default_cwd":str(directory)},"shell":{"program":shell,"args":["-f"]},"server":{"detached":False,"detach_on_quit":False},"ui":{"system_notifications":True}}))
log=(directory/"gui.log").open("w")
gui=subprocess.Popen([water,"--control-socket",socket,"--config",str(config)],stdout=log,stderr=subprocess.STDOUT)

def ctl(*args):
    r=subprocess.run([water,"ctl","--socket",socket,*args],capture_output=True,timeout=5)
    if r.returncode:raise RuntimeError(r.stderr.decode())
    return json.loads(r.stdout)

def wait(predicate,description,seconds=8):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        if gui.poll() is not None:raise RuntimeError((directory/"gui.log").read_text())
        try:
            value=predicate()
            if value:return value
        except (RuntimeError,subprocess.TimeoutExpired):pass
        time.sleep(.025)
    raise RuntimeError("Timeout: "+description+"; artifacts="+str(directory))

try:
    pane=wait(lambda:ctl("ui","snapshot").get("frame_focused_pane"),"GUI ready")
    ctl("pane","input","--pane",pane,"--text","printf '\\a'\n")
    status=wait(lambda:ctl("ui","snapshot").get("native_menu",{}).get("notification_status"),"system notification result",20)
    (directory/"result.json").write_text(json.dumps({"status":status,"gui_pid":gui.pid,"socket":socket},indent=2))
    if status not in ("scheduled","permission_denied"):raise RuntimeError("notification status="+status)
    print("PASS real system notification callback; status="+status+"; artifacts="+str(directory),flush=True)
finally:
    try:ctl("server","shutdown")
    except Exception:pass
    if gui.poll() is None:gui.terminate()
    try:gui.wait(timeout=5)
    except subprocess.TimeoutExpired:gui.kill();gui.wait()
    log.close()
