#!/usr/bin/env python3
"""Start an owned loopback-only sshd and verify real SSH command transport."""
import getpass
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time

directory=Path(tempfile.mkdtemp(prefix="water-ssh-test.",dir="/tmp"))
root=Path(__file__).resolve().parent.parent
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument("--gui",action="store_true",help="also verify native GUI remote pane routing")
options=parser.parse_args()
def run(*args):
    return subprocess.run(args,capture_output=True,text=True,timeout=10,check=True)
for name in ("host","client"):
    run("ssh-keygen","-q","-t","ed25519","-N","","-f",str(directory/name))
(directory/"authorized_keys").write_text((directory/"client.pub").read_text())
with socket.socket() as probe:
    probe.bind(("127.0.0.1",0))
    port=probe.getsockname()[1]
config=directory/"sshd_config"
config.write_text(f"""Port {port}
ListenAddress 127.0.0.1
HostKey {directory}/host
PidFile {directory}/sshd.pid
AuthorizedKeysFile {directory}/authorized_keys
StrictModes no
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
AllowUsers {getpass.getuser()}
AllowTcpForwarding yes
AllowStreamLocalForwarding yes
LogLevel VERBOSE
""")
log=(directory/"sshd.log").open("w")
daemon=subprocess.Popen(["/usr/sbin/sshd","-D","-e","-f",str(config)],stdout=log,stderr=subprocess.STDOUT)
server=None
server_log=None
control=None
client_config=None
alias=None
master_path=None
gui=None
gui_log=None
local_socket=None
try:
    deadline=time.monotonic()+5
    while time.monotonic()<deadline:
        if daemon.poll() is not None: raise RuntimeError((directory/"sshd.log").read_text())
        try:
            with socket.create_connection(("127.0.0.1",port),timeout=.1): break
        except OSError: time.sleep(.025)
    result=run("ssh","-F","/dev/null","-p",str(port),"-i",str(directory/"client"),"-o","BatchMode=yes","-o","StrictHostKeyChecking=no","-o",f"UserKnownHostsFile={directory}/known_hosts",f"{getpass.getuser()}@127.0.0.1","printf WATER_LOCAL_SSH_OK")
    assert result.stdout=="WATER_LOCAL_SSH_OK",result.stdout
    client_config=directory/"ssh_config"
    alias="water-test-"+directory.name
    client_config.write_text(f"""Host {alias}
    HostName 127.0.0.1
    Port {port}
    User {getpass.getuser()}
    IdentityFile {directory}/client
    IdentitiesOnly yes
    BatchMode yes
    StrictHostKeyChecking no
    UserKnownHostsFile {directory}/known_hosts
""")
    hashed=14695981039346656037
    for byte in (alias+"|"+str(client_config)).encode():
        hashed=((hashed ^ byte)*1099511628211)&((1<<64)-1)
    master_path=f"/tmp/water-go-ssh-dev-{os.geteuid()}-{hashed:016x}.ctl"
    remote_socket=str(directory/"remote.sock")
    shell=next(p for p in ("/opt/homebrew/bin/zsh","/bin/zsh") if Path(p).exists())
    server_config=directory/"server.json"
    server_config.write_text(json.dumps({"shell":{"program":shell,"args":["-f"]},"server":{"detached":False}}))
    server_log=(directory/"server.log").open("w")
    server=subprocess.Popen([str(root/"target/go-ui-smoke/water-server"),"--socket",remote_socket,"--config",str(server_config)],stdout=server_log,stderr=subprocess.STDOUT)
    deadline=time.monotonic()+5
    while not Path(remote_socket).exists() and time.monotonic()<deadline:
        if server.poll() is not None: raise RuntimeError((directory/"server.log").read_text())
        time.sleep(.025)
    environment=os.environ.copy()
    environment.update(WATER_TEST_SSH_DESTINATION=alias,WATER_SSH_CONFIG=str(client_config),WATER_REMOTE_CONTROL_SOCKET=remote_socket,WATER_REMOTE_SERVER_COMMAND=str(root/"target/go-ui-smoke/water-server"))
    tested=subprocess.run(["go","test","-race","./internal/goui","-run","TestSSHManagedReconnect","-v","-count=1","-timeout","40s"],cwd=root,env=environment,text=True,capture_output=True,timeout=45)
    if tested.returncode: raise RuntimeError(tested.stdout+tested.stderr)
    print(tested.stdout,end="")
    if options.gui:
        water=str(root/"target/go-ui-smoke/water")
        local_socket=str(directory/"gui.sock")
        gui_config=directory/"gui.json"
        gui_config.write_text(json.dumps({"startup":{"default_cwd":str(directory)},"shell":{"program":shell,"args":["-f"]},"server":{"detached":False,"detach_on_quit":False},"ui":{"sidebar_agent_mode":"split"}}))
        gui_log=(directory/"gui.log").open("w")
        gui=subprocess.Popen([water,"--control-socket",local_socket,"--config",str(gui_config),"--ssh",alias],env=environment,stdout=gui_log,stderr=subprocess.STDOUT)
        def ctl(socket,*args):
            response=subprocess.run([water,"ctl","--socket",socket,*map(str,args)],capture_output=True,text=True,timeout=5)
            if response.returncode:raise RuntimeError(response.stderr)
            return json.loads(response.stdout)
        def wait(predicate,description):
            deadline=time.monotonic()+8
            while time.monotonic()<deadline:
                if gui.poll() is not None:raise RuntimeError((directory/"gui.log").read_text())
                try:
                    value=predicate()
                    if value:return value
                except (RuntimeError,subprocess.TimeoutExpired):pass
                time.sleep(.025)
            raise RuntimeError("Timeout: "+description)
        snapshot=wait(lambda: (lambda s:s if s.get("frame_focused_pane") else None)(ctl(local_socket,"ui","snapshot")),"remote GUI")
        connections=ctl(local_socket,"connections","list")["connections"]
        remote=next(c for c in connections if c["kind"]=="remote")
        local=next(c for c in connections if c["kind"]=="local")
        assert remote["status"]=="connected"
        pane,terminal=snapshot["frame_focused_pane"],snapshot["frame_active_terminal"]
        ctl(remote["socket_path"],"pane","input","--pane",pane,"--text","printf WATER_SSH_GUI_OK\\n\n")
        ctl(remote["socket_path"],"terminal","contains","--terminal",terminal,"WATER_SSH_GUI_OK","--timeout-ms", "5000")
        ctl(remote["socket_path"],"pane","input","--pane",pane,"--text","exec -a opencode /bin/sleep 60\n")
        wait(lambda:ctl(remote["socket_path"],"state").get("agents"),"remote agent")
        def click(kind,id):
            hit=wait(lambda: next((h for h in ctl(local_socket,"ui","snapshot").get("automation_hits",[]) if h["kind"]==kind and h["id"]==id and h["rect"][3]>h["rect"][1]),None),kind)
            x0,y0,x1,y1=hit["rect"]
            ctl(local_socket,"ui","click","--x",(x0+x1)/2,"--y",(y0+y1)/2)
        click("connection",local["id"])
        click("agent",pane)
        wait(lambda:ctl(local_socket,"ui","snapshot")["frame_focused_pane"]==pane,"remote pane focus")
        assert next(c for c in ctl(local_socket,"connections","list")["connections"] if c["kind"]=="remote")["active"]
        ctl(local_socket,"ui","screenshot","--output",str(directory/"remote-agent.png"))
        print("PASS native SSH GUI terminal round-trip and cross-connection Agent pane activation",flush=True)
    print(json.dumps({"result":"PASS","port":port,"owned_sshd_pid":daemon.pid,"artifacts":str(directory)}))
finally:
    if gui is not None:
        subprocess.run([str(root/"target/go-ui-smoke/water"),"ctl","--socket",local_socket,"server","shutdown"],capture_output=True,timeout=3)
        if gui.poll() is None:gui.terminate()
        try:gui.wait(timeout=5)
        except subprocess.TimeoutExpired:gui.kill();gui.wait()
    if gui_log:gui_log.close()
    if master_path and Path(master_path).exists():
        subprocess.run(["ssh","-F",str(client_config),"-S",master_path,"-O","exit",alias],capture_output=True,timeout=3)
    if server is not None:
        subprocess.run([str(root/"target/go-ui-smoke/water"),"ctl","--socket",str(directory/"remote.sock"),"server","shutdown"],capture_output=True,timeout=3)
        if server.poll() is None: server.terminate()
        server.wait(timeout=5)
    if server_log: server_log.close()
    if daemon.poll() is None: daemon.terminate()
    try: daemon.wait(timeout=3)
    except subprocess.TimeoutExpired: daemon.kill(); daemon.wait()
    log.close()
