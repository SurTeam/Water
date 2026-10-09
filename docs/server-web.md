# Server Web access

Water server can expose its workspaces and terminals directly to a browser through a separate HTTP/HTTPS and WebSocket listener. Existing desktop IPC and SSH connections continue to work. Network reachability is supplied by the user, for example with Tailscale; Water does not perform NAT traversal or relay traffic.

## Configure the target server

Settings → Server → Web service opens an independent modal for the currently selected connection. The main Server page keeps only this entry. The modal contains status/start/stop, Web settings, pairing and device management. Close or Escape returns to the original Settings page; underlying settings cannot be clicked while the Web modal is open. On an SSH connection, listener addresses belong to the remote machine. Certificate and key files are selected from the desktop with the native file picker for both local and SSH connections; Apply imports their contents to private files on the selected server and saves that server's configuration. Stop Web before changing the address or certificates.

- HTTP listen address: a hostname/IP without a scheme or port. The default `127.0.0.1` listens on loopback. Set `0.0.0.0` explicitly to bind all IPv4 interfaces. A concrete local IP or hostname restricts the listener to that interface. Do not enter an SSH alias or the desktop's forwarding socket.
- HTTP listen port: a separate integer from 1 to 65535, default `8080`. The saved port is fixed across server restarts. The server derives actual browser access URLs; wildcard listeners advertise a concrete local IP for QR pairing and accept only known local-address origins. `0.0.0.0` is never an invitation's browser address.
- Enable HTTPS (TLS): optional. HTTP/WS is the default; enabling this setting uses HTTPS/WSS and reveals certificate/key fields.
- TLS certificate/key files: HTTPS requires both files. Select a PEM certificate or full chain (`.pem`/`.crt`, leaf certificate first), and its matching unencrypted PEM private key (`.pem`/`.key`; PKCS#8, RSA PKCS#1 or EC). DER certificates, password-encrypted keys and `.p12`/`.pfx` bundles must be converted first. Each file is limited to 256 KiB. Apply validates the pair before changing configuration and imports it into a new private directory (0700, files 0600) on the owning server. Cancelling a picker preserves the draft; HTTP needs no files. Older servers lacking `web-tls-import/v1` must be upgraded before importing. Certificates must cover the browser's access IP/name and chain to a CA the browser trusts; Water does not obtain or renew certificates automatically.
The settings page explains these formats and shows a local file-selection button beside each TLS field. Web actions and secondary Settings buttons have theme-aware borders; Apply uses the same accent treatment as Settings → Save.

- `WATER_CONFIG` selects the configuration file for both GUI and headless server; an explicit `--config` takes precedence. SSH verification uses a private target file and checks that user configuration files remain unchanged.
- Start Web with server: a dedicated switch on the Web service modal. It saves whether Web starts with this server. It can be changed while Web is running and does not restart or interrupt the listener. Start Web and Stop Web affect the current listener without changing this preference.

The listen address and port are saved separately in the target server's configuration. Start the service and use the concrete access URL shown in the Web service modal or its QR code. Isolated verification fixtures reserve random available ports unless an explicit test port is requested.

The same fields are available in local Startup settings, taking effect on server restart. Remote Server settings do not overwrite local Startup preferences.

Example target-server config:

```json
{
  "web": {
    "enabled": false,
    "listen_address": "127.0.0.1",
    "listen_port": 8080,
    "tls": false
  }
}
```

CLI management uses the existing trusted control endpoint. For an SSH-connected remote server, use the forwarding socket shown by `water ctl connections list`:

```sh
water ctl --socket /path/to/control.sock server web configure \
  --listen-address 0.0.0.0 --listen-port 8080 \
  --tls false \
  --enabled false
water ctl --socket /path/to/control.sock server web start
water ctl --socket /path/to/control.sock server web status
water ctl --socket /path/to/control.sock server web pair
water ctl --socket /path/to/control.sock server web devices
water ctl --socket /path/to/control.sock server web revoke DEVICE_UUID
water ctl --socket /path/to/control.sock server web cancel INVITATION_UUID
water ctl --socket /path/to/control.sock server web stop
```

Configure replaces all Web fields. HTTP uses WS and needs no certificates; HTTPS uses WSS and requires `--tls true --cert ... --key ...`. Pairing, authorization, Host/Origin checks and revocation apply to all interfaces. Legacy `public_url` configurations and the CLI `--url` option are migrated to separate listener fields; saving removes `public_url`. Status retains `public_url` as a derived access URL and `listen_address` as the actual bound socket. Configuration is persisted in the target server's `--config`/resolved config path. Pair returns a secret invitation URL; display it to the intended device instead of saving it in shared logs.

## Pair and connect

Start Web, then select Pair device. Open the server address on the phone and select **Scan QR code** to photograph the desktop GUI's QR, or **Choose QR image** to select an existing image. Image loading and QR recognition run locally in this browser with embedded jsQR in a bounded worker; the photo is never uploaded. The page shows a separate progress/result message, tries multiple image scales and a bounded Gaussian smoothing fallback for screen-camera texture, and reports unsupported formats, unreadable codes and timeouts. JPEG/PNG or pasting a pairing link through Add server are available fallbacks. HTTP supports this photo/file flow without requiring live camera permissions. A decoded Water invitation adds its origin to this browser's server list and opens that server's invitation URL; same-server invitations explicitly reload so the pairing fragment is processed. Non-Water QR codes and unsafe URL schemes are rejected. You can also scan the GUI's QR with the phone's system camera. The QR points directly to the target server; the desktop GUI is not a data gateway. On a headless server, open the invitation URL returned by the CLI.

An invitation lasts five minutes and can be exchanged exactly once. Scanning authorizes the browser without another desktop confirmation. The page removes the invitation fragment from the address bar before exchanging it. Closing/cancelling the GUI invitation or switching connections cancels an unused invite; used invites disappear after the panel observes consumption.

The Water title row has a small triangle to collapse or expand the top controls. Collapsing hides Settings, Scan, server selection and terminal selection/actions; the title/status row and keyboard helper footer stay available, and the terminal refits to the extra space. This website remembers the collapsed state. Expanding restores the controls.

The **Server** dropdown lists up to 16 addresses added by this browser. **Add server** accepts an HTTP/HTTPS address or a Water pairing link. Choosing a server navigates directly to that origin; authorization cookies remain independent. Bookmarks store only origins in `water.web.servers`, never invitations or credentials. A bounded validated list of addresses is carried in the navigation fragment when moving between origins, then removed from the address bar; each site saves its own browser-local copy. If website storage is unavailable, selection still works for the current visit.

The resulting device authorization lasts 30 days and survives server restarts. Open the same server URL next time without scanning another code. Reopening a consumed or pre-restart invitation URL also connects when this browser already has a valid device cookie; an unpaired browser still needs a new invitation. Devices can be revoked from Server → Web service → Devices or the CLI. Unpair in the browser revokes that browser's authorization. Revocation disconnects its active WebSockets.

Authorization files store only credential digests in `web-<variant>-config-<sha256-of-absolute-config-path>/devices.json` beside the target config. This private directory (0700) and file (0600) preserve the server's authorization identity independently of its process or control socket path. Writes use sync and atomic rename. The existing socket-named directory for this configuration's current socket is migrated without changing device IDs or cookies; unrelated legacy directories are not guessed or merged. Servers without a config path retain socket-based storage. Dev/release and different configuration files remain isolated. Invites are memory-only and do not survive restart; paired devices remain valid. Do not delete the authorization directory if you want to retain paired devices. A browser cookie is scoped to its hostname; use the same access address after restart.

## Browser behavior and lifecycle

The embedded Web client offers a workspace/tab/pane selector, terminal input/output, new workspaces/tabs, splits, and mobile keyboard helpers. It uses locally embedded xterm.js assets, with no CDN or external analytics. Each browser has its own selection and emulator. The page follows the visual viewport height and top offset so mobile keyboard helpers remain above the on-screen keyboard while the terminal shrinks to fit. Keyboard dismissal and orientation changes restore the layout; helper buttons preserve terminal focus. Replay restores retained output after reload/reconnection; history older than the server's bounded replay is explicitly marked as unavailable.

**Hide keyboard** blurs terminal input; **Show keyboard** restores focus from the user's tap. While explicitly hidden, auxiliary keys and closing Settings do not refocus terminal input. This keeps iOS users able to dismiss the keyboard without sending an Escape or other dummy key to the shell.

The browser's **Settings → Font** dialog offers system defaults and common system monospace fonts detected on this device. A selection applies immediately to the page, preview and terminal; the terminal remeasures its grid to fit. No font files are downloaded and no font-enumeration permission is requested. The choice is stored in `localStorage` under `water.web.settings` for this website (scheme, host and port), and is restored after reload, reconnect and opening another tab. Unavailable or invalid saved fonts fall back to the system default. When website storage is blocked, changes still apply for the current visit. This preference does not change desktop or server configuration.

Resize follows the existing session focus ownership rule. The server retains PTYs when a browser disconnects or Web is stopped. An enabled Web listener keeps an embedded server alive after its last desktop window closes, so a later direct browser connection remains possible. Explicit server shutdown/restart and host shutdown still stop sessions. A paired remote browser does not require the original desktop or its SSH connection to remain online.

The GUI's existing capability details remain available under Capabilities. A server without `web-access/v1` shows an upgrade message instead of Web management controls.

## Protocol and access boundary

The Web listener provides `/`, static client assets, `/api/pair`, `/api/session`, `/api/logout`, and `/ws`. HTTP/WS or HTTPS/WSS terminates in Water server. Origin/Host checks, HttpOnly SameSite cookies and one-time invites protect the browser endpoint. HTTPS uses Secure cookies with a `__Host-` prefix; HTTP uses a separate cookie name without Secure so browsers can retain authorization on a LAN IP. HSTS is emitted only for HTTPS. Browser sessions cannot invoke Web administration, recovery, GUI automation, internal commands or server shutdown. Authorized terminal input executes with the target server user's shell privileges.

The WS adapter shares the existing session/dispatcher/model path. Binary WebSocket messages carry chunks of the existing 32-bit-length-prefixed Water stream; a message need not contain a complete frame. Live terminal events retain WT4 UUID/uint64 layouts. Browser attach replay serializes `seq`, `first_seq` and `last_seq` as decimal strings to avoid JavaScript integer truncation. Native IPC/SSH replay encoding remains unchanged.

Limits include 32 concurrent WebSockets, 64 authorized devices, 32 pending invites, 60 pairing attempts per minute, 1 MiB per inbound WebSocket message and the existing 16 MiB Water frame cap. Writes have deadlines, browser parsing/output buffers are bounded, and stalled/disconnected clients can reattach using replay. No terminal output is silently dropped to satisfy backpressure.

## Verification

Core tests include HTTPS certificate validation, atomic invite consumption, expiry, Origin rejection, permission boundaries, real shell input/output, device revocation/persistence, server lifecycle and GUI connection routing. Browser fixtures also verify font selection, preview and terminal application, restoration after reload/reconnect/new tab, invalid or blocked storage, mobile dialog bounds and the absence of font downloads.

```sh
~/.venv/bin/python scripts/run-go-regression.py --profile unit --list
~/.venv/bin/python scripts/run-go-regression.py --profile unit
# With WATER_BIN and WATER_SERVER_BIN pointing to current builds:
~/.venv/bin/python scripts/go-web-browser-smoke.py
~/.venv/bin/python scripts/go-web-browser-smoke.py --ssh
# Requires a real native desktop/display:
~/.venv/bin/python scripts/go-ui-web-smoke.py
# Plain HTTP on an actual local IPv4 interface, with no certificate or trust bypass:
~/.venv/bin/python scripts/go-ui-web-smoke.py --http --host 192.168.15.177
```

Browser fixtures require Playwright plus Chromium (or explicit `WATER_BROWSER_EXECUTABLE`), cryptography and OpenCV. They use isolated profiles and temporary certificates; browser certificate trust is bypassed only in these fixtures. Go HTTPS tests explicitly trust and verify their test certificate. The native GUI fixture decodes the actual Draw screenshot's QR before pairing. The SSH fixture uses an owned loopback OpenSSH daemon and verifies direct Web operation after SSH ends. Headless browser checks do not replace native GUI/QR drawing or real-phone verification.

The native macOS fixture opens the actual file panel and completes/cancels it through Water's control API, then verifies the imported credentials on the target server. Optional `WATER_TLS_CERT_FILE`, `WATER_TLS_KEY_FILE` and `WATER_TLS_CA_FILE` exercise an existing CA-issued pair and a separate HTTPS request that verifies the root chain and hostname without ignoring certificate errors. Exported evidence excludes private imported TLS directories.

Vendored frontend versions: `@xterm/xterm` 5.5.0 and `@xterm/addon-fit` 0.10.0 from npm. Their license files are embedded alongside the assets; upgrades must preserve the licenses and rerun browser verification. QR image recognition uses vendored `jsqr` 1.4.0 from npm, verified against the package SHA-512 integrity and distributed with its Apache-2.0 license.
