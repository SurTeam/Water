#!/usr/bin/env python3
"""Owned native GUI + Chromium + real SSH Web pairing/direct-access regression."""
import argparse
from datetime import datetime, timedelta, timezone
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import sys
import time
import ssl
import urllib.request
import urllib.error
import cv2
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from playwright.sync_api import sync_playwright, expect
from water_test import WaterGUI, ControlError
from fixtures.web_viewport import install_visual_viewport_fixture, verify_keyboard_helpers
from fixtures.web_fonts import verify_web_fonts

root = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("owned_web_ssh", root / "scripts/fixtures/openssh_server.py")
ssh_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ssh_module)


def certificate(directory):
    key = ec.generate_private_key(ec.SECP256R1())
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "Water owned test")])
    cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name).public_key(key.public_key())
            .serial_number(x509.random_serial_number()).not_valid_before(datetime.now(timezone.utc) - timedelta(minutes=1))
            .not_valid_after(datetime.now(timezone.utc) + timedelta(hours=1))
            .add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address("127.0.0.1"))]), critical=False)
            .sign(key, hashes.SHA256()))
    cert_path, key_path = directory / "web-cert.pem", directory / "web-key.pem"
    cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    key_path.chmod(0o600)
    return cert_path, key_path


def click(test, label):
    hit = test.wait(lambda: next((h for h in test.ctl("ui", "snapshot")["automation_hits"] if h.get("label") == label), None), "click " + label)
    x0, y0, x1, y1 = hit["rect"]
    assert test.ctl("ui", "click", "--x", (x0 + x1) / 2, "--y", (y0 + y1) / 2)["handled"]


def operation(test, label):
    click(test, "server:" + label)
    state = test.wait(lambda: (lambda s: s if not s["busy"] else None)(test.ctl("ui", "snapshot")["server_status"]), label)
    assert state["message"] == "Server operation completed", state["message"]
    return state


def edit(test, name, value):
    click(test, "field:Web." + name)
    assert test.ctl("ui", "key", "cmd-a" if platform.system() == "Darwin" else "ctrl-a")["handled"]
    assert test.ctl("ui", "key", "text:" + str(value))["handled"]


def select_tls_file(test, kind, path):
    click(test, "server:web-select-" + kind)
    test.wait(lambda: test.ctl("ui", "snapshot")["native_menu"].get("file_picker_open"), "native local file picker opens")
    # Selection travels through Water's control API and the native picker
    # completion path, with no system input injection or secret contents.
    assert test.ctl("ui", "menu", "--action", "file-picker-select:" + str(path))["handled"]
    state = test.wait(lambda: (lambda s: s if not s["busy"] else None)(test.ctl("ui", "snapshot")["server_status"]), "selected TLS file read")
    assert state["message"] == "Server operation completed", state["message"]


def endpoint_ctl(binary, endpoint, *args):
    result = subprocess.run([binary, "ctl", "--socket", endpoint, *map(str, args)], capture_output=True, text=True, timeout=5)
    if result.returncode:
        raise ControlError(result.stderr.strip())
    return json.loads(result.stdout)


def exercise(test, browser, control, kind, disconnect_ssh=None, scheme="https", host="127.0.0.1", port=None, config_path=None):
    cert, key = certificate(test.directory) if scheme == "https" else ("", "")
    if scheme == "https" and os.environ.get("WATER_TLS_CERT_FILE"):
        cert, key = Path(os.environ["WATER_TLS_CERT_FILE"]), Path(os.environ["WATER_TLS_KEY_FILE"])
    with socket.socket() as reservation:
        # Match Go's listener reuse semantics when sequential owned fixtures use
        # the same explicit port; a closed previous listener can leave TIME_WAIT.
        if port is not None:
            reservation.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        reservation.bind((host, port or 0))
        port = reservation.getsockname()[1]
    origin = f"{scheme}://{host}:{port}"
    if not test.ctl("ui", "snapshot")["settings_visible"]:
        assert test.ctl("ui", "key", "cmd-,")["handled"]
    click(test, "category:Server")
    main = test.ctl("ui", "snapshot")
    assert any(h.get("label") == "server:web-open" for h in main["automation_hits"])
    assert not any(h.get("label") in ("server:web-start", "server:web-settings", "server:web-pair") for h in main["automation_hits"]), "Web controls still crowd Server settings"
    test.ctl("ui", "screenshot", "--output", test.directory / (kind + "-server-entry.png"))
    operation(test, "web-open")
    modal = test.ctl("ui", "snapshot")
    assert modal["server_status"]["web_visible"] and modal["settings_visible"]
    assert not any(h.get("label", "").startswith("category:") for h in modal["automation_hits"]), "underlying Settings remained clickable"
    assert test.ctl("ui", "key", "escape")["handled"]
    test.wait(lambda: not test.ctl("ui", "snapshot")["server_status"]["web_visible"], "Escape closes Web modal")
    assert test.ctl("ui", "snapshot")["settings_visible"], "Escape closed underlying Settings"
    operation(test, "web-open")
    operation(test, "web-settings")
    assert control("server", "web", "status")["config"]["listen_address"] == "127.0.0.1", "default should be loopback"
    test.ctl("ui", "screenshot", "--output", test.directory / (kind + "-default-web-settings.png"))
    listen_address = "0.0.0.0" if scheme == "http" else host
    edit(test, "ListenAddress", listen_address)
    edit(test, "ListenPort", str(port))
    if scheme == "https":
        click(test, "field:Web.TLS")
        select_tls_file(test, "cert", cert)
        select_tls_file(test, "key", key)
        assert {"server:web-select-cert", "server:web-select-key"}.issubset({h.get("label") for h in test.ctl("ui", "snapshot")["automation_hits"]})
        click(test, "server:web-select-key")
        test.wait(lambda: test.ctl("ui", "snapshot")["native_menu"].get("file_picker_open"), "native picker opens for cancellation")
        assert test.ctl("ui", "menu", "--action", "file-picker-cancel")["handled"]
        test.wait(lambda: not test.ctl("ui", "snapshot")["server_status"]["busy"], "native picker cancellation")
    hits = test.ctl("ui", "snapshot")["automation_hits"]
    labels = {h.get("label") for h in hits}
    assert {"field:Web.ListenAddress", "field:Web.ListenPort"}.issubset(labels)
    assert "field:Web.PublicURL" not in labels, "obsolete single address input"
    test.ctl("ui", "screenshot", "--output", test.directory / (kind + "-web-settings.png"))
    operation(test, "web-configure")
    settings = control("server", "web", "status")
    assert settings["config"]["listen_address"] == listen_address and settings["config"]["listen_port"] == port
    assert settings["config"]["tls"] == (scheme == "https")
    if scheme == "https":
        assert settings["config"]["tls_cert_file"] != str(cert), "local selected certificate path leaked to target"
        assert settings["config"]["tls_key_file"] != str(key), "local selected key path leaked to target"
        assert Path(settings["config"]["tls_key_file"]).stat().st_mode & 0o777 == 0o600
        assert Path(settings["config"]["tls_cert_file"]).read_bytes() == Path(cert).read_bytes()
        (test.directory / (kind + "-tls-import-assertions.json")).write_text(json.dumps({"native_picker_opened": True, "selected_on_desktop": True, "target_owned_private_files": True, "config_persisted_on_target": True, "direct_https": True}, indent=2))
    owned_config = Path(config_path) if config_path else test.directory / "config.json"
    persisted_web = json.loads(owned_config.read_text())["web"]
    assert persisted_web["listen_address"] == listen_address and persisted_web["listen_port"] == port
    assert persisted_web["tls"] == (scheme == "https"), "configuration was not saved to the owned target file"
    assert settings["status"]["public_url"] == ("" if scheme == "http" else origin)
    assert "public_url" not in settings["config"], "obsolete editable URL retained"
    assert settings["status"]["listen_address"] == f"{listen_address}:{port}"
    assert settings["status"]["state"] == "stopped"
    operation(test, "web-start")
    running = control("server", "web", "status")["status"]
    assert running["state"] == "running"
    assert running["listen_address"] == f"{listen_address}:{port}"
    origin = running["public_url"]
    if scheme == "https" and os.environ.get("WATER_TLS_CA_FILE"):
        trusted = ssl.create_default_context(cafile=os.environ["WATER_TLS_CA_FILE"])
        try:
            urllib.request.urlopen(origin + "/api/session", context=trusted, timeout=5)
            raise AssertionError("unpaired request unexpectedly accepted")
        except urllib.error.HTTPError as response:
            assert response.code == 401
        (test.directory / (kind + "-ca-handshake.json")).write_text(json.dumps({"root_and_hostname_verified": True, "certificate_errors_ignored": False, "unpaired_status": 401}, indent=2))
    assert "0.0.0.0" not in origin, "wildcard address appeared in access URL"
    operation(test, "web-autostart")
    saved = control("server", "web", "status")
    assert saved["config"]["enabled"] and saved["status"]["public_url"] == origin
    assert saved["status"]["state"] == "running", "autostart toggle interrupted Web"
    test.ctl("ui", "screenshot", "--output", test.directory / (kind + "-web-modal.png"))
    operation(test, "web-autostart")
    saved = control("server", "web", "status")
    assert not saved["config"]["enabled"] and saved["status"]["public_url"] == origin
    operation(test, "web-pair")
    screenshot = test.directory / (kind + "-qr.png")
    test.ctl("ui", "screenshot", "--output", screenshot)
    image = cv2.imread(str(screenshot))
    invitation, points, _ = cv2.QRCodeDetector().detectAndDecode(image)
    assert invitation.startswith(origin + "/#pair="), "rendered QR does not contain the target server's direct URL"
    assert points is not None
    context = browser.new_context(ignore_https_errors=scheme == "https", viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True)
    install_visual_viewport_fixture(context)
    page = context.new_page()
    resize_events = []
    def watch_socket(websocket):
        pending = bytearray()
        def frame_received(payload):
            if not isinstance(payload, bytes):
                return
            pending.extend(payload)
            assert len(pending) <= 2 * 1024 * 1024, 'test frame buffer exceeded bound'
            while len(pending) >= 4:
                length = int.from_bytes(pending[:4], 'big')
                assert length <= 1024 * 1024, 'oversized test protocol frame'
                if len(pending) < length + 4:
                    return
                frame = bytes(pending[4:length + 4])
                del pending[:length + 4]
                if len(frame) >= 33 and frame[:5] == b'\x00WT4\x02':
                    resize_events.append(int.from_bytes(frame[29:31], 'big'))
                    del resize_events[:-64]
        websocket.on('framereceived', frame_received)
    page.on('websocket', watch_socket)
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.goto(origin, timeout=10000)
    expect(page.locator('#status')).to_contain_text('Not paired', timeout=5000)
    expect(page.locator('#pairing-panel')).to_be_visible()
    expect(page.locator('#scan-qr')).to_be_visible()
    page.screenshot(path=str(test.directory / (kind + '-unpaired.png')))
    blank = image.copy()
    blank[:] = 255
    blank_path = test.directory / (kind + '-blank.png')
    assert cv2.imwrite(str(blank_path), blank)
    with page.expect_file_chooser() as chooser:
        page.locator('#choose-qr').click()
    chooser.value.set_files(str(blank_path))
    expect(page.locator('#scan-message')).to_contain_text('No QR code found', timeout=15000)
    invalid = cv2.QRCodeEncoder_create().encode("javascript:alert('unexpected')")
    invalid = cv2.copyMakeBorder(invalid, 4, 4, 4, 4, cv2.BORDER_CONSTANT, value=255)
    invalid_path = test.directory / (kind + '-invalid-qr.png')
    assert cv2.imwrite(str(invalid_path), cv2.resize(invalid, (400, 400), interpolation=cv2.INTER_NEAREST))
    dialogs = []
    page.on('dialog', lambda dialog: (dialogs.append(dialog.message), dialog.dismiss()))
    with page.expect_file_chooser() as chooser:
        page.locator('#choose-qr').click()
    chooser.value.set_files(str(invalid_path))
    expect(page.locator('#scan-message')).to_contain_text('not a Water pairing QR code', timeout=15000)
    assert page.url == origin + '/' and not dialogs, 'non-Water QR was followed'
    with page.expect_file_chooser() as chooser:
        page.locator('#scan-qr').click()
    with page.expect_navigation(timeout=15000):
        chooser.value.set_files(str(screenshot))
    expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
    expect(page.locator('#pairing-panel')).to_be_hidden()
    assert page.url == origin + "/", "pairing secret left in the address bar"
    assert page.locator("#panes option").count() >= 1
    test.wait(lambda: test.ctl("ui", "snapshot")["server_status"].get("pairing_expires_at") is None, "invitation consumed")
    # A consumed QR also works again for an already authorized browser.
    with page.expect_file_chooser() as chooser:
        page.locator('#scan-qr').click()
    with page.expect_navigation(timeout=15000):
        chooser.value.set_files(str(screenshot))
    expect(page.locator('#status')).to_have_text('Connected', timeout=10000)
    marker = "WEB_" + kind.upper() + "_ROUNDTRIP"
    page.locator(".xterm-helper-textarea").focus()
    page.keyboard.type("printf 'WEB_%s\\n' " + kind.upper() + "_ROUNDTRIP", delay=1)
    page.keyboard.press("Enter")
    expect(page.locator('.xterm-accessibility-tree')).to_contain_text(marker, timeout=5000)
    page.screenshot(path=str(test.directory / (kind + "-browser.png")))
    # Reload restores the server's replay into a new browser emulator.
    page.reload(timeout=10000)
    expect(page.locator('.xterm-accessibility-tree')).to_contain_text(marker, timeout=5000)
    page.bring_to_front()
    page.locator('.xterm-helper-textarea').focus()
    selected_terminal = page.locator('#panes').input_value()
    control('terminal', 'resize', '--terminal', selected_terminal, '--columns', 160, '--lines', 40)
    test.wait(lambda: page.evaluate('true') and 160 in resize_events, 'browser receives foreign PTY resize')
    page.wait_for_function("() => {const s=document.querySelector('.xterm-screen').getBoundingClientRect(),t=document.getElementById('terminal').getBoundingClientRect();return s.width>0 && s.width<=t.width+1 && s.height<=t.height+1;}", timeout=5000)
    test.save(kind + '-foreign-resize-assertions.json', {'received_columns': 160, 'mobile_viewport_width': 390, 'refitted_after_foreign_resize': True})
    verify_web_fonts(page, test.directory, kind, marker)
    verify_keyboard_helpers(page, test.directory, kind)
    if disconnect_ssh:
        # The browser stays connected directly after the owning SSH transport ends.
        disconnect_ssh()
        page.locator(".xterm-helper-textarea").focus()
        page.keyboard.type("printf 'DIRECT_%s\\n' AFTER_SSH", delay=1)
        page.keyboard.press("Enter")
        expect(page.locator('.xterm-accessibility-tree')).to_contain_text('DIRECT_AFTER_SSH', timeout=5000)
        assert context.request.get(origin + "/api/session").status == 200
    else:
        count = page.locator("#panes option").count()
        page.locator("#split").click()
        expect(page.locator('#panes option')).to_have_count(count+1, timeout=5000)
        assert test.ctl("ui", "snapshot")["server_status"]["server"]["web"]["state"] == "running"
        operation(test, "web-devices")
        device = test.wait(lambda: next(iter(test.ctl("ui", "snapshot")["server_status"].get("web_devices", [])), None), "paired device")
        operation(test, "web-revoke:" + device["id"])
        assert context.request.get(origin + "/api/session").status == 401
        operation(test, "web-close")
        operation(test, "web-stop")
        assert control("server", "web", "status")["status"]["state"] == "stopped"
        assert control("state")["workspaces"], "Stop Web destroyed the workspace"
    assert not errors, errors
    test.save(kind + "-assertions.json", {"wildcard_http_listener": scheme == "http", "reused_qr_with_saved_authorization": True, "web_fonts_saved": True, "web_modal": True, "modal_escape_returns": True, "autostart_toggle": True, "port_preserved": True, "paired": True, "qr_decoded": True, "web_scan_button": True, "invalid_qr_rejected": True, "keyboard_toolbar_visible": True, "keyboard_helper_focus": True, "replay_restored": True, "direct_after_ssh": bool(disconnect_ssh), "browser_errors": errors, "tls": "ephemeral certificate; browser trust bypass only in this fixture" if scheme == "https" else "plain HTTP; no certificates or trust bypass", "origin": origin})
    context.close()
    # Store an already consumed QR as evidence, never log the pairing URL.
    print("PASS " + kind + " GUI Web pairing/browser terminal/replay; artifacts=" + str(test.directory), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--local-only", action="store_true")
    parser.add_argument("--remote-only", action="store_true")
    parser.add_argument("--http", action="store_true", help="verify plain HTTP and ws without certificates")
    parser.add_argument("--host", default="127.0.0.1", help="local IPv4 interface used by this owned fixture")
    parser.add_argument("--language", choices=("en", "zh-Hans"), default="en", help="native GUI language")
    parser.add_argument("--port", type=int, help="explicit test Web port; default reserves an isolated random port")
    parser.add_argument("--variant", choices=("dev", "release"), default="dev", help="runtime identity of the tested GUI/server")
    args = parser.parse_args()
    if args.port is not None and not 1 <= args.port <= 65535:
        parser.error("--port must be between 1 and 65535")
    ipaddress.IPv4Address(args.host)
    options = {"scheme": "http" if args.http else "https", "host": args.host, "port": args.port}
    if platform.system() == "Linux" and not (os.getenv("DISPLAY") or os.getenv("WAYLAND_DISPLAY")):
        raise RuntimeError("native display is required")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, executable_path=os.environ.get("WATER_BROWSER_EXECUTABLE"))
        try:
            if not args.remote_only:
                with WaterGUI("water-web-local.", config={"ui": {"language": args.language}}, variant=args.variant) as test:
                    exercise(test, browser, test.ctl, "local", **options)
            if not args.local_only:
                with ssh_module.OpenSSHServer(variant=args.variant) as ssh:
                    value = 14695981039346656037
                    for byte in (ssh.alias + "|" + str(ssh.directory / "ssh_config")).encode():
                        value = ((value ^ byte) * 1099511628211) & ((1 << 64) - 1)
                    ssh.control = f"/tmp/water-go-ssh-{args.variant}-{os.geteuid()}-{value:016x}.ctl"
                    os.environ["WATER_REMOTE_SERVER_COMMAND"] = os.environ["WATER_SERVER_BIN"]
                    with WaterGUI("water-web-remote.", config={"ui": {"language": args.language}}, variant=args.variant) as test:
                        assert test.ctl("ui", "key", "cmd-shift-k")["handled"]
                        assert test.ctl("ui", "key", "text:" + ssh.alias)["handled"]
                        assert test.ctl("ui", "key", "enter")["handled"]
                        entry = test.wait(lambda: next((e for e in test.ctl("connections", "list")["connections"] if e["kind"] == "remote" and e["status"] == "connected"), None), "real SSH connection")
                        control = lambda *a: endpoint_ctl(test.binary, entry["socket_path"], *a)
                        info = control("server", "info")
                        ssh.record_remote(info)
                        test.wait(lambda: test.ctl("ui", "snapshot").get("frame_focused_pane") == control("state")["focused_pane"], "remote GUI frame")
                        local_before = test.ctl("server", "web", "status")
                        def disconnect():
                            result = subprocess.run(["/usr/bin/ssh", "-F", str(ssh.directory / "ssh_config"), "-S", ssh.control, "-O", "exit", ssh.alias], capture_output=True, text=True, timeout=5)
                            assert result.returncode == 0, result.stderr
                        exercise(test, browser, control, "remote", disconnect, config_path=ssh.directory / "water-config.json", **options)
                        assert test.ctl("server", "web", "status")["config"] == local_before["config"], "remote Web settings overwrote local settings"
                        test.save("remote-ownership.json", {"server_pid": info["server_pid"], "instance_id": info["instance_id"], "socket": info["socket_path"], "sshd_pid": ssh.sshd.pid})
        finally:
            browser.close()


if __name__ == "__main__":
    main()
