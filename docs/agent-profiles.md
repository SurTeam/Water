# Agent profiles

Open **Settings → Agents** on the connection that owns the terminal. The page
shows which local or remote server is being edited. Definitions are saved to
that server's configuration; appearance preferences elsewhere in Settings do
not replace them.

Builtin Agents are templates. Editing one saves only changed fields; **Restore
defaults** removes its override. Builtins can be disabled. **New** creates a
custom profile with a UUIDv4, **Duplicate** copies the selected profile to a new
UUID, and **Delete** removes a custom profile. Save applies the whole Agent
draft. Cancel requires explicit discard when the draft has changed. A save is
rejected if another window changed the server's definitions after the page was
loaded; cancel and reopen to load the current version. Saving first samples
live foreground processes; a sampling or persistence failure leaves the saved
configuration and its revision unchanged and reports an error.

Names, colors, recognition rules, OSC interpretation, clear-screen history and
long-run thresholds apply to existing terminals. Sampling runs in the server
background every 500 ms. Recognition is independent of OSC titles and output
text. Existing custom pane names and OSC titles retain their normal display
precedence over the profile's default name. Automatic tab titles use the
profile name while the real foreground process name remains available for
recognition and diagnostics.

## Recognition rules

The recognition field contains a JSON array of rules. **Edit…** opens a guided
editor with explanations for `all`, `field`, `op`, and `value`. Use **Add rule**
and **Add condition** to build alternatives and combined conditions; click the
`field` and `op` buttons to cycle their supported choices. Type the match value
directly, without JSON escaping. **Apply to JSON** validates the draft and uses
standard JSON serialization to preserve quotes, backslashes and literal text.
It fills the settings draft; **Save** still applies it to the selected server.
Cancel or Escape closes the editor without changing the JSON. The raw JSON
field remains available for manual edits. Rule values are compared as data and
are never evaluated as shell commands.

Any matching rule selects the profile; every condition in a rule's `all` array
must match. Example for a Node-based Agent:

```json
[
  {
    "all": [
      {"field": "executable", "op": "exact", "value": "node"},
      {"field": "path", "op": "contains", "value": "/my-agent/cli.js"}
    ]
  }
]
```

| Field | Compared values |
| --- | --- |
| `executable` | Basename of the observed foreground command's argv[0] |
| `name` | Normalized process/argv[0] name, executable or script basenames, and supported `exec -a` aliases |
| `path` | Foreground arguments containing `/`, including executable/script paths |
| `argument` | Any sampled foreground argument token |

Operators are `exact`, `contains` and `glob`. Name comparisons are insensitive to
case; other fields preserve case. Globs use Go's filepath pattern syntax, where
`*` does not cross path separators. Process arguments come from bounded `ps`
sampling: shell quoting and arguments containing spaces are not reconstructed
perfectly. Use distinctive executable/script paths when possible, and test
against the actual process on the target host.

Higher numeric priority wins when several enabled profiles match. Equal
priorities use the stable profile ID as the tie breaker. **Test recognition**
uses the focused terminal and the current unsaved draft, showing sampled PID,
command and arguments, all matching profiles and the selected rule. Testing
does not save the draft or change runtime classification.

## Status and controls

**Status source** selects:

- `auto`: OSC reports when available, otherwise the process lifecycle.
- `osc`: Unknown until an OSC report is available; process exit still wins.
- `process`: Running/Exited lifecycle, ignoring OSC reports.

A process-only Running status indicates that the program exists. It does not
establish that the Agent is working on a task. OSC state 4 can mean a pause or
warning; it does not establish that approval is required.

**OSC states 0,1,2,3,4** contains five comma-separated presentation states. The
default is `Idle,Running,Error,Running,Paused`. Supported values are `Idle`,
`Running`, `Error`, `Paused`, `Completed`, and `Unknown`. Clear the field to use
the default mapping. No hooks are installed, and no output text is interpreted
as an Agent state event.

**Interrupt preset** offers `ctrl-c`, `sigint`, or `none`. **Exit preset** offers
`sigterm`, `sighup`, `ctrl-d`, or `none`. The page's **Interrupt** and **Request
exit** buttons act on the focused terminal using its saved profile. Select the profile
that matches the focused terminal before using these buttons. Save edited
presets before testing an action. `none` disables that action. A request does
not automatically escalate to SIGKILL. Closing a pane continues to use terminal
session cleanup, including its shell and descendants.

Control commands carry the expected run identity. Before sending terminal input
or a signal, the target server checks PID, process group, start information and
sampled arguments, then rechecks that the group is this PTY's foreground group.
A changed run, changed Agent classification, or another terminal's identity is rejected. Signals target the
foreground group; they never use an arbitrary PID supplied by a profile.

**Long-run seconds** overrides the Agent notification/indicator threshold;
`0` inherits the global Agent threshold. **Keep history on clear** selects the
existing client-side clear-screen policy. Profile color overrides the legacy
Theme Agent color; an empty color inherits that theme preference.

## CLI and import/export

Commands use the selected control socket, including a socket forwarded to a
remote server:

```sh
water ctl --socket /tmp/water-dev.sock agent list
water ctl --socket /tmp/water-dev.sock agent export > agents.json
water ctl --socket /tmp/water-dev.sock agent configure --file agents.json
water ctl --socket /tmp/water-dev.sock agent test --pane PANE_UUID
water ctl --socket /tmp/water-dev.sock agent test --pane PANE_UUID --file agents.json
water ctl --socket /tmp/water-dev.sock agent interrupt --pane PANE_UUID
water ctl --socket /tmp/water-dev.sock agent exit --pane PANE_UUID
water ctl --socket /tmp/water-dev.sock agent force-stop --pane PANE_UUID
```

`--terminal TERMINAL_UUID` can replace `--pane`. Omitting a target uses the
server's active pane. Force-stop is an explicit CLI action using SIGKILL, with
the same run ownership checks. Regular terminal Ctrl+C/Ctrl+D input remains
available independently of the profile action presets.

The standalone import/export file is an Agent configuration object, with
`overrides` and `custom` arrays. It is not an entire Water application config.
Definitions are limited to 128 custom profiles/overrides and 256 KiB of compact
configuration JSON. Unknown fields and invalid definitions are rejected. Import replaces the saved
Agent configuration. An empty object restores all builtin defaults and removes
custom definitions. In the application config, the same object lives under
`agents` (or `overrides.agents` for an override envelope).

The server advertises `agent-profiles/v1`. Update an older server through the
existing Server compatibility/recovery flow before using these settings.
Arbitrary action scripts and custom state event transports are outside this
first implementation.
