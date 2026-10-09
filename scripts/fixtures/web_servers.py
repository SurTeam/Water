"""Browser-local photo decode and direct navigation between owned servers."""
import json
import socket
import subprocess
from pathlib import Path
from urllib.parse import urlparse

import cv2
import numpy as np
from playwright.sync_api import expect


def verify_servers(page, context, directory, binary, server_binary, primary_origin, wait):
    with socket.socket() as reserve:
        reserve.bind(('127.0.0.1', 0))
        port = reserve.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    endpoint = str(directory / 'second.sock')
    config = directory / 'second-config.json'
    shell = next(p for p in ('/opt/homebrew/bin/zsh', '/bin/zsh') if Path(p).is_file())
    config.write_text(json.dumps({'startup': {'default_cwd': str(directory)},
        'shell': {'program': shell, 'args': ['-f']},
        'web': {'enabled': True, 'listen_address': '127.0.0.1', 'listen_port': port, 'tls': False}}))
    log = (directory / 'second-server.log').open('w')
    process = subprocess.Popen([server_binary, '--socket', endpoint, '--config', str(config)],
                              stdout=log, stderr=log, start_new_session=True)
    requests = []
    def request_seen(request):
        if request.method == 'POST':
            requests.append({'path': urlparse(request.url).path,
                             'content_type': request.headers.get('content-type', '')})
    page.on('request', request_seen)
    def ctl(*args):
        result = subprocess.run([binary, 'ctl', '--socket', endpoint, *args],
                                capture_output=True, text=True, check=True, timeout=5)
        return json.loads(result.stdout)
    def ready():
        if process.poll() is not None:
            raise RuntimeError('second server exited')
        try:
            info = ctl('server', 'info')
            assert info['server_pid'] == process.pid
            return info
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            return None
    try:
        info = wait(ready, 'second server readiness')
        (directory / 'second-ownership.json').write_text(json.dumps({'pid': process.pid,
            'instance_id': info['instance_id'], 'socket': endpoint, 'config': str(config)}, indent=2))
        assert context.request.get(origin + '/api/session').status == 401
        invitation = ctl('server', 'web', 'pair')['url']
        qr = cv2.QRCodeEncoder_create().encode(invitation)
        qr = cv2.copyMakeBorder(qr, 4, 4, 4, 4, cv2.BORDER_CONSTANT, value=255)
        qr = cv2.resize(qr, (600, 600), interpolation=cv2.INTER_NEAREST)
        photo = np.full((3024, 4032), 230, dtype=np.uint8)
        photo[1212:1812, 1716:2316] = qr
        photo = cv2.GaussianBlur(photo, (3, 3), .3)
        path = directory / 'camera-photo.jpg'
        assert cv2.imwrite(str(path), photo, [cv2.IMWRITE_JPEG_QUALITY, 85])
        assert page.evaluate('typeof createImageBitmap') == 'undefined', 'image compatibility fixture missing'
        with page.expect_file_chooser() as chooser:
            page.locator('#scan-qr').click()
        with page.expect_navigation(timeout=15000):
            chooser.value.set_files(str(path))
        expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
        assert page.url == origin + '/'
        assert context.request.get(origin + '/api/session').status == 200
        expect(page.locator('#servers option')).to_have_count(2)
        saved = page.evaluate("localStorage.getItem('water.web.servers')")
        assert invitation.split('#pair=')[1] not in saved and '#pair' not in saved
        assert set(json.loads(saved)['origins']) == {origin, primary_origin}
        page.reload(timeout=10000)
        expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
        expect(page.locator('#servers option')).to_have_count(2)
        with page.expect_navigation(timeout=10000):
            page.locator('#servers').select_option(primary_origin)
        expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
        assert page.url == primary_origin + '/'
        assert set(page.locator('#servers option').evaluate_all('(options) => options.map(o=>o.value)')) == {origin, primary_origin}
        page.locator('#server-add').click()
        page.locator('#server-address').fill(origin)
        with page.expect_navigation(timeout=10000):
            page.get_by_role('button', name='Open server', exact=True).click()
        expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
        assert page.url == origin + '/'
        with page.expect_navigation(timeout=10000):
            page.locator('#servers').select_option(primary_origin)
        expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
        assert all(r['path'] == '/api/pair' and 'application/json' in r['content_type'] for r in requests), requests
        page.screenshot(path=str(directory / 'server-selector.png'))
        (directory / 'server-selector-assertions.json').write_text(json.dumps({
            'large_jpeg_decoded_locally': True, 'createImageBitmap_unavailable': True,
            'cross_server_qr_navigation': True, 'separate_authorization': True,
            'dropdown_switch_and_reload': True, 'manual_address_navigation': True,
            'no_persisted_invitation_secret': True, 'no_photo_upload': True,
            'post_requests': requests}, indent=2))
    finally:
        page.remove_listener('request', request_seen)
        if Path(endpoint).exists():
            assert ctl('server', 'info')['server_pid'] == process.pid
            subprocess.run([binary, 'ctl', '--socket', endpoint, 'server', 'shutdown'],
                           capture_output=True, check=True, timeout=5)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.terminate()  # Exact Popen-owned instance only.
            process.wait(timeout=5)
        log.close()
        (directory / 'second-cleanup.json').write_text(json.dumps({
            'process_exited': process.poll() is not None, 'socket_removed': not Path(endpoint).exists()}, indent=2))
