#!/usr/bin/env python3
"""Owned Windows desktop smoke. Run in an interactive logged-on session."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import time
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--core-only', action='store_true', help='Run lifecycle/recovery only; report clipboard as not selected')
    args = parser.parse_args()
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=True)
    binary = str(Path(args.binary).resolve())
    token = 'water-win-' + uuid.uuid4().hex[:12]
    socket = str(out / (token + '.sock'))
    config = out / 'config.json'
    config.write_text(json.dumps({'startup': {'default_cwd': str(out)}, 'shell': {'program': 'powershell.exe', 'args': ['-NoLogo', '-NoProfile']}, 'server': {'detached': False, 'detach_on_quit': False}}))
    env = dict(os.environ, WATER_TEST_INSTANCE=token, WATER_TEST_UNFOCUSED='1', WATER_CONTROL_SOCKET=socket, WATER_CONFIG=str(config), WATER_PTY_SUPERVISOR_SOCKET=str(out / (token + '-pty.sock')))
    records = []
    gui = None
    def ctl(*command):
        result = subprocess.run([binary, 'ctl', '--socket', socket, *command], env=env, capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=12)
        records.append(dict(command=list(command), code=result.returncode, stdout=result.stdout, stderr=result.stderr))
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        try:
            value = json.loads(result.stdout)
            if isinstance(value, dict) and value.get('status') in ('failed', 'rejected'):
                raise RuntimeError(str(value))
            if isinstance(value, dict) and value.get('status') == 'pending':
                return ctl('operation', 'wait', value.get('operation_id') or value['id'])
            return value
        except ValueError:
            return result.stdout
    def wait(query):
        deadline = time.monotonic() + 25
        last = None
        while time.monotonic() < deadline:
            try:
                value = query()
                if value:
                    return value
            except Exception as error:
                last = error
            time.sleep(.1)
        raise RuntimeError('condition deadline: ' + str(last))
    passed = False
    try:
        with (out / 'gui.log').open('w') as log:
            gui = subprocess.Popen([binary, '--control-socket', socket, '--config', str(config)], env=env, stdout=log, stderr=subprocess.STDOUT)
        (out / 'ownership.json').write_text(json.dumps(dict(gui_pid=gui.pid, socket=socket, config=str(config), instance=token)))
        wait(lambda: ctl('ping'))
        snapshot = wait(lambda: (lambda s: s if s.get('frame_focused_pane') else None)(ctl('ui', 'snapshot')))
        if snapshot.get('test_instance') != token:
            raise RuntimeError('GUI ownership mismatch')
        (out / 'first-frame.json').write_text(json.dumps(snapshot, indent=2))
        pane = snapshot['frame_focused_pane']
        ctl('server', 'info')
        ctl('state')
        ctl('pane', 'content', '--pane', pane)
        marker = 'WATER_NATIVE_' + uuid.uuid4().hex
        # Construct output distinct from the echoed command line.
        ctl('pane', 'input', '--pane', pane, '--text', "Write-Output ('" + marker[:15] + "' + '" + marker[15:] + "')\r")
        wait(lambda: marker in ctl('ui', 'content', '--pane', pane).get('text', ''))
        ctl('pane','input','--pane',pane,'--text',"Write-Output ('WATER_CJK_'+[char]0x4e2d+[char]0x6587)\r")
        wait(lambda: 'WATER_CJK_中文' in ctl('ui','content','--pane',pane).get('text',''))
        ctl('ui', 'screenshot', '--output', str(out / 'native-frame.png'))
        if (out / 'native-frame.png').stat().st_size < 100:
            raise RuntimeError('empty screenshot')
        # Guarded recovery must preserve the live PowerShell, not only layout.
        recovery_mark = uuid.uuid4().hex
        ctl('pane', 'input', '--pane', pane, '--text', "$env:WATER_RECOVERY_MARK='" + recovery_mark + "'; Write-Output ('RECOVERY_'+'ARMED')\r")
        wait(lambda: 'RECOVERY_ARMED' in ctl('ui', 'content', '--pane', pane).get('text', ''))
        original = ctl('server', 'info')
        def click(label):
            hit = wait(lambda: next((h for h in ctl('ui', 'snapshot').get('automation_hits', []) if h.get('label') == label), None))
            x0,y0,x1,y1 = hit['rect']
            if not ctl('ui', 'click', '--x', str((x0+x1)/2), '--y', str((y0+y1)/2)).get('handled'):
                raise RuntimeError('unhandled UI control: ' + label)
        ctl('ui', 'key', 'ctrl-,')
        click('category:Server')
        click('server:backup')
        saved = wait(lambda: (lambda v: v if not v['busy'] and v['recovery_path'] else None)(ctl('ui','snapshot')['server_status']))
        recovery = json.loads(Path(saved['recovery_path']).read_text(encoding='utf-8-sig'))
        if recovery['pending']:
            raise RuntimeError('backup is pending')
        click('server:restart')
        if ctl('server','info')['instance_id'] != original['instance_id']:
            raise RuntimeError('restart bypassed confirmation')
        click('server:restart')
        replacement = wait(lambda: (lambda v: v if v['server_status']['server']['instance_id'] != original['instance_id'] and not v['server_status']['busy'] and v.get('frame_focused_pane') == pane else None)(ctl('ui','snapshot')))
        if not replacement['server_status']['compatibility']['compatible']:
            raise RuntimeError('replacement server incompatible')
        ctl('ui','key','escape')
        ctl('pane','input','--pane',pane,'--text',"Write-Output ('RECOVERED_' + $env:WATER_RECOVERY_MARK)\r")
        wait(lambda: 'RECOVERED_'+recovery_mark in ctl('ui','content','--pane',pane).get('text',''))
        (out / 'recovery.json').write_text(json.dumps({'old_instance':original['instance_id'],'new_instance':replacement['server_status']['server']['instance_id'],'shell_environment_preserved':True,'backup':saved['recovery_path']},indent=2))
        ctl('ui','screenshot','--output',str(out/'recovered-frame.png'))
        if args.core_only:
            (out / 'clipboard.json').write_text(json.dumps({'status':'not-selected','suite':'core'}))
        else:
            from windows_clipboard_fixture import temporary_text, read_text
            clip_marker = 'WATER_CLIP_' + uuid.uuid4().hex
            text = "Write-Output ('" + clip_marker[:12] + "' + '" + clip_marker[12:] + "')"
            with temporary_text(text) as clipboard:
                ctl('ui', 'key', 'ctrl-shift-v')
                ctl('ui', 'key', 'enter')
                wait(lambda: clip_marker in ctl('ui', 'content', '--pane', pane).get('text', ''))
                content = ctl('ui', 'content', '--pane', pane)
                row = next(i for i,line in enumerate(content['lines']) if line.rstrip() == clip_marker)
                grid = next(g for g in ctl('ui','snapshot')['terminal_grids'] if g['pane_id'] == pane)
                x,y = grid['rect'][:2]
                cw,lh = grid['cell_width'],grid['cell_height']
                ctl('ui','drag','--x',str(x+.1*cw),'--y',str(y+(row+.5)*lh),'--to-x',str(x+(len(clip_marker)-.1)*cw),'--to-y',str(y+(row+.5)*lh))
                ctl('ui','key','ctrl-shift-c')
                wait(lambda: read_text() == clip_marker)
                clipboard['paste'] = 'passed'
                clipboard['copy'] = 'passed'

            clipboard['restored'] = True
            (out / 'clipboard.json').write_text(json.dumps(clipboard))
        if not args.core_only:
            # The application's settings are driven through ctl. Only the OS
            # common dialog is driven with UI Automation, scoped to this PID.
            ctl('ui','key','ctrl-,')
            click('category:Server')
            click('server:web-open')
            click('server:web-settings')
            snapshot = ctl('ui','snapshot')
            if not any(h.get('label') == 'server:web-select-cert' for h in snapshot.get('automation_hits',[])):
                click('field:Web.TLS')
            certificate = out / 'native-picker-fixture.pem'
            certificate.write_bytes((Path(__file__).resolve().parent.parent / 'tests/fixtures/windows/file-picker.pem').read_bytes())
            configuration_before = config.read_bytes()
            picker_results=[]
            for cancel in (False,True):
                click('server:web-select-cert')
                command=['powershell.exe','-NoProfile','-ExecutionPolicy','Bypass','-File',str(Path(__file__).with_name('windows-file-picker-driver.ps1')),'-OwnedProcessId',str(gui.pid),'-ExpectedTitle','Choose PEM certificate / chain']
                command += ['-Cancel'] if cancel else ['-FixturePath',str(certificate)]
                result=subprocess.run(command,capture_output=True,text=True,encoding='utf-8',errors='replace',timeout=30)
                if result.returncode:
                    raise RuntimeError('native picker driver: '+result.stderr)
                picker_results.append(json.loads(result.stdout))
                status=wait(lambda: (lambda v: v if not v['busy'] else None)(ctl('ui','snapshot')['server_status']))
                if status['message'] != 'Server operation completed':
                    raise RuntimeError('native picker result: '+status['message'])
                ctl('ui','screenshot','--output',str(out/('picker-cancel.png' if cancel else 'picker-select.png')))
            if config.read_bytes() != configuration_before:
                raise RuntimeError('file picker selection/cancellation changed saved configuration')
            (out/'native-picker.json').write_text(json.dumps({'passed':True,'configuration_preserved':True,'results':picker_results},indent=2))
            click('server:web-close')
            ctl('ui','key','escape')
            for _ in range(2):
                if not ctl('ui','snapshot').get('settings_visible'):
                    break
                click('cancel')
            wait(lambda: not ctl('ui','snapshot').get('settings_visible'))
            if config.read_bytes() != configuration_before:
                raise RuntimeError('discarding picker draft changed saved configuration')
        ctl('pane', 'split', '--pane', pane, '--right')
        ctl('pane', 'resize', '--pane', pane, '--ratio', '0.6')
        ctl('ui','screenshot','--output',str(out/'split-frame.png'))
        ctl('pane', 'close', '--pane', pane)
        ctl('ui', 'snapshot')
        passed = True
    finally:
        if gui is not None and gui.poll() is None:
            try:
                # The socket/config and test instance above identify this embedded server.
                ctl('ui', 'menu', 'quit-and-server')
            except Exception:
                pass
            try:
                gui.wait(timeout=8)
            except subprocess.TimeoutExpired:
                gui.terminate()
                gui.wait(timeout=8)
        # Stop only the supervisor bearing this run's unguessable socket token.
        script = "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains('--pty-supervisor') -and $_.CommandLine.Contains('" + token + "-pty.sock') } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop }"
        cleanup = subprocess.run(['powershell.exe', '-NoProfile', '-EncodedCommand', base64.b64encode(script.encode('utf-16le')).decode()], capture_output=True, text=True, timeout=10)
        (out / 'cleanup.json').write_text(json.dumps(dict(gui_exited=gui.poll() is not None if gui else True, supervisor_cleanup_code=cleanup.returncode, stderr=cleanup.stderr)))
        if cleanup.returncode or (gui is not None and gui.returncode != 0):
            passed = False
        (out / 'summary.json').write_text(json.dumps(dict(passed=passed, suite='core' if args.core_only else 'all', gui_pid=gui.pid if gui else None, gui_exit=gui.poll() if gui else None, records=records), indent=2))

    if not passed:
        raise RuntimeError('owned GUI shutdown or cleanup failed; see evidence')


if __name__ == '__main__':
    # Match the fixture's per-user exclusion on a platform without flock.
    import msvcrt
    lock_path = Path(os.environ['LOCALAPPDATA']) / 'Water' / 'desktop-tests.lock'
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open('a+b') as lock:
        lock.seek(0, 2)
        if not lock.tell():
            lock.write(b'\0')
            lock.flush()
        lock.seek(0)
        deadline = time.monotonic() + 60
        while True:
            try:
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
                break
            except OSError:
                if time.monotonic() >= deadline:
                    raise RuntimeError('another Windows desktop test owns the fixture lock')
                time.sleep(.1)
        try:
            main()
        finally:
            lock.seek(0)
            msvcrt.locking(lock.fileno(), msvcrt.LK_UNLCK, 1)

