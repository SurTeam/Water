# Server compatibility and recovery

Settings → **Server** applies to the selected Local or Remote connection. It shows GUI/server versions, the server source revision, and a matrix of capabilities each side provides or requires. Refresh, Save layout, Restart server and Retry restore use that connection's transport. Restart requires a second click after the warning about ending running tasks.

## Admission and build identity

`server.inspect` is a read-only metadata RPC with stable framing. It can be used across application protocol versions; the dev/release variant check still applies. `session.open` carries the GUI descriptor. GUI and server both assess protocol, API signature and required/provided capabilities before normal workspace operations. Incompatible sessions remain diagnostic sessions: both the client transport and the server reject normal model/terminal operations.

Product version is informational. Packaged GUI/server/payloads receive the same `gobuild.ServerRevision`, computed by `scripts/server-revision.py` from server dependency source files and go.mod/go.sum. Product-version overrides are excluded. Changes confined to GUI packages do not change this fingerprint; shared protocol/configuration changes do. An implementation revision difference prompts a restart even if the API remains usable.

Plain `go build` uses a development revision marker. For revision-sensitive development tests, supply the fingerprint linker flag, use the packaging scripts, or use the regression runner. Server executables are checked for variant, protocol, API signature and server revision before launch. Local restart checks that a matching sibling is available **before** stopping the existing server; stale sibling names are skipped rather than selected blindly.

`water ctl client info` exposes the GUI descriptor. `water ctl server info` uses the stable inspection RPC with a bounded legacy fallback, retaining the established windows list.

## Remote discovery

Default remote endpoints are stable across product and protocol upgrades:

```
/tmp/water-go-<variant>-<destination-hash>.sock
```

Payload caches remain scoped by product version, server revision, protocol and platform. Discovery probes the stable endpoint first, then the previous `water-go-<variant>-p*-*-<destination-hash>.sock` endpoints. A discoverable old instance is reused, including in diagnostic mode; it is not replaced automatically. Ambiguous or uninspectable legacy instances produce an error instead of starting an unrelated fresh workspace. `WATER_REMOTE_CONTROL_SOCKET` selects a particular endpoint explicitly.

Remote restart preserves the discovered endpoint, including a legacy endpoint. Replacement GUI views receive fresh forwarding sockets so cancelling the old forward does not remove the replacement's endpoint. Health reconnect and explicit server operations are serialized per connection.

## Recovery transaction

1. `recovery.prepare` checks the expected server instance and refuses another GUI window or another active recovery lease. It freezes dispatched changes, exports topology, queries current directories on the owning server, and verifies the layout revision did not change. Directory collection has a total deadline.
2. The GUI projects its own workspace/tab/pane selection into the exported layout. It validates and atomically persists a private recovery artifact, syncing the file and directory before requesting shutdown. Failed saves cancel the lease and leave the server running.
3. `recovery.shutdown` checks the owning session, random token, expiry, window count and model revision. Ordinary quit/shutdown remains distinct from this guarded restart path.
4. The replacement starts empty. `command.dispatch` → `recovery.restore` validates the complete document and prepares new shells before adopting the model. Missing directories or failed spawns remove the prepared PTYs and leave an empty model.
5. The GUI replaces the connection view, preserves its selection/settings, and marks the artifact completed. Repeated import of the same recovery ID into the same server is idempotent.

The schema retains workspace/tab/pane identities and order, names and title overrides, split axes/ratios, empty panes, active selections, terminal geometry, labels and current directories. Restored terminals and terminal surfaces receive new UUIDv4 identities. Shells use the replacement server's configured shell. Running jobs/Agents, terminal replay/history and process memory are not resumed.

Artifacts live under `recovery/` beside the GUI config, keyed by variant and logical endpoint; files are mode 0600. Pending artifacts are retried at startup/reconnect, but never imported into an unrelated nonempty model. A failed start/import retains the artifact and exposes Retry restore. An invalid existing artifact is not silently overwritten.

Legacy servers lacking advertised recovery support cannot reliably export live directories. Their established API baseline can still be reused, but Settings explicitly disables automatic recovery restart. No new GUI can retroactively add an export API to an old running server.

## Verification and ownership

`go-ui-server-smoke.py` exercises both Local and Remote settings/restart paths through real Water actions. Its SSH stand-in uses actual Unix forwarding and actual server processes on this machine.

`go-ui-real-ssh-smoke.py` repeats the Local/Remote recovery assertions using an owned OpenSSH daemon on a random loopback port. It verifies key authentication and strict host-key checking, the default embedded payload detection/upload/start path, encrypted Unix socket forwarding, ControlMaster loss/reconnect, stable endpoint reuse, and GUI product-version upgrades. It uses an isolated forced-command configuration for the test server; it does not enable Remote Login or modify system SSH configuration. Host/client private keys are removed before exporting logs, including on test failure. This is a real SSH network connection to a separate server process on the same macOS host; native Linux runtime behavior remains unverified.

Run it with revision-matched dev binaries and refreshed payloads:

```sh
WATER_BIN=/path/to/owned/water-test-gui \
WATER_SERVER_BIN=/path/to/owned/water-srv-dev \
WATER_UPGRADE_BIN=/path/to/owned/new-product-version-gui \
WATER_TEST_EVIDENCE_DIR=/path/to/test-evidence \
~/.venv/bin/python scripts/go-ui-real-ssh-smoke.py
```

`WATER_UPGRADE_BIN` is optional; providing it checks a different GUI product version against the unchanged server revision.

The reusable GUI fixture sets a unique socket/config and `WATER_TEST_INSTANCE`; dev windows visibly say **Water Test**. It records existing Water process identities before launch. Before shutdown it checks socket, variant, instance/PID ownership and process arguments, and refuses cleanup when ownership cannot be proven. Cleanup records whether pre-existing Water processes were preserved. Regression builds use unique per-run binary directories.

Use the unit regression profile for transport/model/race coverage and the owned native server smoke for GUI behavior. Test evidence includes commands, PIDs, exit codes, screenshots, state/content, recovery documents and cleanup results. Never target a user's existing Water socket to exercise restart.
