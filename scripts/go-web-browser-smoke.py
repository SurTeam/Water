#!/usr/bin/env python3
"""Real browser/server Web verification without claiming native GUI coverage."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tempfile
import time
from playwright.sync_api import sync_playwright, expect
from fixtures.web_fonts import verify_web_fonts
from fixtures.web_servers import verify_servers
from fixtures.web_mobile_controls import verify_mobile_controls

root=Path(__file__).resolve().parent.parent
spec=importlib.util.spec_from_file_location("web_gui_helpers",root/"scripts/go-ui-web-smoke.py")
helpers=importlib.util.module_from_spec(spec);spec.loader.exec_module(helpers)


def wait(predicate,description,timeout=5):
    deadline=time.monotonic()+timeout
    while True:
        result=predicate()
        if result:return result
        if time.monotonic()>deadline:raise RuntimeError("timeout: "+description)
        time.sleep(.025)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ssh",action="store_true")
    args=parser.parse_args()
    binary=os.environ.get("WATER_BIN",str(root/"target/go-ui-smoke/water"))
    server_binary=os.environ.get("WATER_SERVER_BIN",str(root/"target/go-ui-smoke/water-server"))
    directory=Path(tempfile.mkdtemp(prefix="water-web-browser.",dir="/tmp"))
    cert,key=helpers.certificate(directory)
    with socket.socket() as reserve:
        reserve.bind(("127.0.0.1",0));port=reserve.getsockname()[1]
    origin=f"https://127.0.0.1:{port}"
    endpoint=str(directory/"control.sock")
    config=directory/"config.json"
    shell=next(p for p in ("/opt/homebrew/bin/zsh","/bin/zsh") if Path(p).is_file())
    config.write_text(json.dumps({"startup":{"default_cwd":str(directory)},"shell":{"program":shell,"args":["-f"]},"web":{"enabled":True,"listen_address":"127.0.0.1","listen_port":port,"tls":True,"tls_cert_file":str(cert),"tls_key_file":str(key)}}))
    process=None;forward=None;ssh=None;browser=None
    ownership={"socket":endpoint,"config":str(config),"tls":"ephemeral test certificate; browser trust bypass only"}
    log=(directory/"server.log").open("w")
    try:
        if args.ssh:
            ssh=helpers.ssh_module.OpenSSHServer();ssh.__enter__()
            value=14695981039346656037
            for byte in ssh.alias.encode():value=((value^byte)*1099511628211)&((1<<64)-1)
            endpoint=f"/tmp/water-go-dev-{value:016x}.sock"
            command="nohup "+shlex.quote(server_binary)+" --socket "+shlex.quote(endpoint)+" --config "+shlex.quote(str(config))+" >"+shlex.quote(str(directory/"server.log"))+" 2>&1 </dev/null & echo $!"
            pid=int(ssh.run(command).stdout.strip())
            ownership.update(server_pid=pid,sshd_pid=ssh.sshd.pid,socket=endpoint)
        else:
            process=subprocess.Popen([server_binary,"--socket",endpoint,"--config",str(config)],stdout=log,stderr=log,start_new_session=True)
            ownership["server_pid"]=process.pid
        def ctl(*a):return helpers.endpoint_ctl(binary,endpoint,*a)
        def ready():
            if process and process.poll() is not None:raise RuntimeError("server exited: "+(directory/"server.log").read_text())
            try:
                result=subprocess.run([binary,"ctl","--socket",endpoint,"server","info"],capture_output=True,text=True,timeout=2)
            except subprocess.TimeoutExpired:
                # Only startup readiness tolerates a transient probe timeout.
                # wait() still enforces its overall deadline; runtime calls fail.
                return None
            return json.loads(result.stdout) if result.returncode==0 else None
        info=wait(ready,"server readiness")
        assert info["server_pid"]==ownership["server_pid"]
        ownership["instance_id"]=info["instance_id"]
        if ssh:
            ssh.record_remote(info)
            forwarded=str(directory/'ssh-forward.sock')
            forward=subprocess.Popen(['/usr/bin/ssh','-F',str(ssh.directory/'ssh_config'),'-o','ExitOnForwardFailure=yes','-N','-L',forwarded+':'+endpoint,ssh.alias],stdout=log,stderr=log,start_new_session=True)
            ownership['ssh_forward_pid']=forward.pid
            def forward_ready():
                if forward.poll() is not None:raise RuntimeError('SSH forward exited: '+(directory/'server.log').read_text())
                result=subprocess.run([binary,'ctl','--socket',forwarded,'server','info'],capture_output=True,text=True,timeout=2)
                return result.returncode==0
            wait(forward_ready,'SSH forwarding readiness')
            ctl=lambda *a: helpers.endpoint_ctl(binary,forwarded,*a)
        (directory/"ownership.json").write_text(json.dumps(ownership,indent=2))
        with sync_playwright() as p:
            browser=p.chromium.launch(headless=True,executable_path=os.environ.get("WATER_BROWSER_EXECUTABLE"))
            context=browser.new_context(ignore_https_errors=True,viewport={"width":390,"height":844},is_mobile=True,has_touch=True)
            context.add_init_script('window.createImageBitmap = undefined;')
            page=context.new_page();errors=[];page.on("pageerror",lambda e:errors.append(str(e)))
            try:
                assert context.request.get(origin+"/api/session").status==401
                invitation=ctl("server","web","pair")
                page.goto(invitation["url"],timeout=10000)
                expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
                assert page.url==origin+"/"
                assert ctl("server","web","devices")
                page.locator(".xterm-helper-textarea").focus()
                page.keyboard.type("printf 'BROWSER_%s\\n' ROUNDTRIP",delay=1);page.keyboard.press("Enter")
                expect(page.locator('.xterm-accessibility-tree')).to_contain_text('BROWSER_ROUNDTRIP', timeout=5000)
                page.reload(timeout=10000)
                expect(page.locator('.xterm-accessibility-tree')).to_contain_text('BROWSER_ROUNDTRIP', timeout=5000)
                if not ssh:
                    paired_id=ctl("server","web","devices")[0]["id"]
                    previous={"pid":process.pid,"socket":endpoint,"instance_id":ownership["instance_id"]}
                    subprocess.run([binary,"ctl","--socket",endpoint,"server","shutdown"],check=True,capture_output=True,timeout=5)
                    process.wait(timeout=5)
                    wait(lambda:not Path(endpoint).exists(),"old server socket removed")
                    endpoint=str(directory/"restarted.sock")
                    process=subprocess.Popen([server_binary,"--socket",endpoint,"--config",str(config)],stdout=log,stderr=log,start_new_session=True)
                    ownership.update(server_pid=process.pid,socket=endpoint,previous_servers=[previous])
                    restarted=wait(ready,"replacement server readiness")
                    assert restarted["instance_id"]!=previous["instance_id"]
                    ownership["instance_id"]=restarted["instance_id"]
                    (directory/"ownership.json").write_text(json.dumps(ownership,indent=2))
                    assert context.request.get(origin+"/api/session").status==200, "paired cookie lost after server restart/socket change"
                    assert ctl("server","web","devices")[0]["id"]==paired_id
                # Reopening a consumed invitation must reuse this browser's
                # persistent authorization rather than failing the whole boot.
                page.goto(invitation["url"],timeout=10000)
                page.reload(timeout=10000)
                expect(page.locator('#status')).to_have_text('Connected', timeout=5000)
                page.locator('.xterm-helper-textarea').focus()
                page.keyboard.type("printf 'BROWSER_%s\\n' ROUNDTRIP",delay=1);page.keyboard.press("Enter")
                expect(page.locator('.xterm-accessibility-tree')).to_contain_text('BROWSER_ROUNDTRIP',timeout=5000)
                verify_web_fonts(page,directory,"browser","BROWSER_ROUNDTRIP")
                verify_servers(page,context,directory,binary,server_binary,origin,wait)
                verify_mobile_controls(page,directory)
                count=page.locator("#panes option").count();page.locator("#split").click()
                expect(page.locator('#panes option')).to_have_count(count+1, timeout=5000)
                if ssh:
                    # End this exact owned SSH forwarding connection; the
                    # browser continues directly to the remote Web endpoint.
                    forward.terminate();forward.wait(timeout=5)
                    page.locator(".xterm-helper-textarea").focus()
                    page.keyboard.type("printf 'DIRECT_%s\\n' AFTER_SSH",delay=1);page.keyboard.press("Enter")
                    expect(page.locator('.xterm-accessibility-tree')).to_contain_text('DIRECT_AFTER_SSH', timeout=5000)
                page.screenshot(path=str(directory/"browser.png"))
                direct_ctl=lambda *a: helpers.endpoint_ctl(binary,endpoint,*a)
                device=direct_ctl("server","web","devices")[0]
                direct_ctl("server","web","revoke",device["id"])
                assert context.request.get(origin+"/api/session").status==401
                direct_ctl("server","web","stop")
                assert direct_ctl("state")["workspaces"]
                assert not errors,errors
                (directory/"assertions.json").write_text(json.dumps({"paired":True,"restart_pairing_persisted":not bool(ssh),"consumed_invite_reused_existing_authorization":True,"terminal_roundtrip":True,"replay":True,"split":True,"revoked":True,"stop_retains_pty":True,"direct_after_ssh":bool(ssh),"browser_errors":errors},indent=2))
            except BaseException:
                (directory/"failure.json").write_text(json.dumps({"status":page.locator('#status').inner_text(),"errors":errors},indent=2))
                page.screenshot(path=str(directory/"failure.png"))
                raise
            finally:
                browser.close();browser=None
    finally:
        if browser:browser.close()
        error=None
        if Path(endpoint).exists():
            try:
                info=helpers.endpoint_ctl(binary,endpoint,"server","info")
                if info["server_pid"]!=ownership.get("server_pid"):raise RuntimeError("server ownership changed")
                subprocess.run([binary,"ctl","--socket",endpoint,"server","shutdown"],check=True,capture_output=True,timeout=5)
                wait(lambda:not Path(endpoint).exists(),"owned server cleanup")
            except BaseException as e:error=str(e)
        if process:
            try:process.wait(timeout=5)
            except subprocess.TimeoutExpired:process.terminate();process.wait(timeout=5)
        if forward and forward.poll() is None:forward.terminate();forward.wait(timeout=5)
        if ssh:
            if not Path(endpoint).exists():ssh.remote_socket=None
            ssh.close()
        log.close()
        (directory/"cleanup.json").write_text(json.dumps({"socket_removed":not Path(endpoint).exists(),"error":error},indent=2))
        print("browser verification artifacts="+str(directory),flush=True)
        if error:raise RuntimeError(error)
    print("PASS real browser/server Web"+(" + real SSH independence" if args.ssh else ""),flush=True)


if __name__=="__main__":main()
