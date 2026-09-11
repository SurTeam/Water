---
name: water-control
description: >-
  Control and verify a running Water terminal GUI through its control socket.
  Use for inspecting workspaces, tabs, panes, and terminals; sending or reading
  terminal text; exercising GUI actions; taking screenshots; and running Water
  scenarios. Do not use for generic shell work outside Water.
---

# Water control

Use the Water control CLI for all interaction with a running Water GUI/server.
This skill is for the control path, not for manipulating the host window with
native input injection.

## Safety and scope

- Use 'water ctl ...' (or the resolved bundle executable below) for Water GUI
  interaction. Never use 'xdotool', 'xte', AppleScript keystroke injection, or
  another system-input workaround.
- Discover state before acting. Treat workspace, tab, pane, terminal, and
  surface IDs as opaque strings; in current builds they are UUIDs, not numeric
  IDs. Never substitute a 'surface_id' for a 'pane_id'.
- Terminal input is shell code executed in the target pane. Send only the
  command the user requested or a small, clearly benign verification command.
  Do not send destructive, privileged, network, or repository-mutating
  commands without explicit user authorization.
- 'server shutdown', workspace/tab/pane close, surface replacement, terminal
  spawning, and terminal input are state-changing. Do not perform them merely
  to diagnose a connection. If the control socket is unavailable, report it;
  do not start a second Water instance or kill an existing one.

## Resolve the CLI and socket

The documented interface is 'water ctl <command>'. Prefer the command on PATH;
if it is not installed there, use the executable inside the running app bundle,
normally '<Water app>.app/Contents/MacOS/water-dev' for a dev build. Do not
assume that 'water-srv-dev' is the control client. Keep the bundle path quoted
because app paths commonly contain spaces.

Socket resolution, from highest to lowest priority:

1. '--socket PATH' or '--socket=PATH'
2. 'WATER_CONTROL_SOCKET'
3. 'server.socket_path' in the Water config
4. 'startup.control_socket' in the Water config
5. '/tmp/water-dev.sock' for dev, or '/tmp/water.sock' for release

Run 'ping' first. A missing bundle executable, missing socket, or failed ping
is a connection/setup failure—not a reason to launch or terminate Water.
When using a full bundle path, replace 'water ctl' in the examples with
'"<Water app>.app/Contents/MacOS/water-dev" ctl'.

~~~sh
water ctl ping
water ctl server info
~~~

## Discover the current tab and its panes

Run 'state' once and parse the JSON; do not rely on screen position or a stale
ID. The state normally contains workspaces, tabs, an active workspace/tab/pane,
and a tab tree. Walk the active tab's tree and flatten its terminal leaves.
Each terminal leaf provides a 'pane_id' and a terminal object or
'terminal_id'; use the pane ID with 'pane ...' commands and the terminal ID
with 'terminal ...' commands.

To target “the other pane”:

1. Select the user-mentioned tab. If no tab is mentioned, use the active tab in
   the active workspace.
2. Read that tab's 'active_pane'.
3. Collect terminal leaves in the same tab and exclude the active pane.
4. If exactly one terminal pane remains, use it. If there are several, use an
   explicit label/position from the request; otherwise report the candidates
   instead of guessing.
5. Read the target pane with 'pane content' before sending input. Confirm that
   it is a terminal surface and that the content looks like a live prompt.

Useful read-only commands:

~~~sh
water ctl state
water ctl pane content --pane <PANE_ID> --columns 120
water ctl terminal snapshot --terminal <TERMINAL_ID>
~~~

'pane content' returns JSON in current builds, including fields such as
'pane_id', 'terminal_id', 'text', and 'lines'. For verification, inspect the
non-blank tail of 'lines' rather than dumping an entire screen into the
conversation.

## Terminal round-trip

For a smoke test, use a unique harmless marker and include a real line ending.
In zsh/bash, $'...\n' is a convenient way to pass the Enter key to the
control CLI; a single-quoted '\n' may be sent as two literal characters.

~~~sh
water ctl pane input \
  --pane <TARGET_PANE_ID> \
  --text $'echo WATER_ROUNDTRIP_OK\n'

# Exit status 0 means the marker was found; the response may be a terminal
# snapshot rather than a boolean.
water ctl terminal contains \
  --terminal <TARGET_TERMINAL_ID> \
  WATER_ROUNDTRIP_OK \
  --timeout-ms 5000

water ctl pane content --pane <TARGET_PANE_ID> --columns 120
~~~

Accept the round-trip only when all of these are true:

- input dispatch succeeds;
- the bounded 'contains' check succeeds; and
- a fresh pane read contains the exact marker in the target terminal's recent
  output.

Use a generated marker when an old screen could contain the same text. Prefer
'terminal contains' or 'operation wait' with a bounded timeout over fixed
'sleep' calls. If a dispatch response is pending rather than 'succeeded', use
'operation wait <ID>' and stop with the returned error if it fails.

For a command that is already associated with a terminal, 'terminal send
--terminal <TERMINAL_ID> <text>' is also valid. Use 'pane input' when the pane
is the reliable identifier.

## GUI actions and screenshots

Use the control interface for real GUI actions:

~~~sh
water ctl ui state
water ctl ui key cmd-t
water ctl ui wheel --x 400 --y 300 --dx 0 --dy 500
water ctl ui screenshot --output /tmp/water-check.png
~~~

Use an explicit screenshot output path, and verify the file when the user asks
for visual confirmation. Do not replace a screenshot or UI state query with
native desktop automation.

## Command groups

| Group | Commands |
| --- | --- |
| Connection/state | 'ping', 'server info', 'state' |
| UI | 'ui state', 'ui key', 'ui wheel', 'ui screenshot' |
| Workspace/tab | 'workspace list/new/activate/rename/close', 'tab activate/new/rename/close' |
| Pane | 'pane content/input/focus/split/resize/close', 'pane rename-agent', 'pane move-to-workspace' |
| Terminal | 'terminal send/spawn/contains/snapshot/wait-exit/resize/scroll' |
| Async operations | 'operation get', 'operation wait' |
| Repeatable tests | 'scenario run <PATH>' |

Dispatch commands return JSON with an operation/result status. Check the CLI
exit status and the returned status; do not treat printed JSON alone as proof
that an action completed.

## Isolated verification

For tests that must not touch the user's running Water, assign a unique
'WATER_CONTROL_SOCKET', point 'WATER_CONFIG' or '--config' at a temporary
config, and record the PID of the instance started by the test. Clean up only
that owned PID after verifying ownership. Never use 'pkill water' or a broad
process match.

Prefer an existing scenario file for repeatable interactions. Use direct CLI
commands for a one-off check; add a scenario step only when the interaction is
expected to be replayed.

## Failure handling

Stop and report the smallest useful diagnosis for these cases:

- CLI executable not found: resolve the app-bundle executable;
- ping/socket failure: check the explicit socket/config, but do not launch or
  kill Water;
- state has no matching active tab or target pane: report the discovered IDs;
- pane is not a terminal or its terminal has exited: do not send input;
- dispatch/contains/wait times out or returns an error: preserve the error and
  avoid unbounded retries.
