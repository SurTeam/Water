# Agent 识别与控制

实现入口：`internal/goagent/`、`internal/goconfig/config.go`、`internal/goserver/agents.go` 与 `internal/goui/` 的 Agent Settings/侧栏路径。识别基于前台进程与 argv，不读取私有会话文件或把输出文本猜成状态；Agent 集成只负责识别、状态显示和测试，不提供发送信号或终端输入的控制操作。

本文记录当前实现与限制；开发讨论先留在上下文，用户确认实现并要求 commit 后再按最终代码更新，见 [工作约定](../AGENTS.md#文档更新时机)。

Open **Settings → Agents** on the connection that owns the terminal. The page
shows which local or remote server is being edited. Definitions are saved to
that server's configuration; appearance preferences elsewhere in Settings do
not replace them.

Builtin Agents are Claude Code, Codex, Pi Coding Agent, and OpenCode. Retired builtin overrides remain dormant in saved configuration for compatibility and do not participate in recognition. The left list uses Previous page/Next page without changing the selection; click an entry to select it.

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

Recognition rules are edited through **Edit recognition rules…**, which opens
a condition list using readable field and comparison names. Click a condition to edit it; longer lists use bounded pages so editing and Apply/Cancel remain visible. Use **Add rule** and **Add condition** to build alternatives and combined conditions; click the field and comparison buttons to cycle their choices.
Type match values directly, without JSON escaping. **Apply rules** validates
and updates the settings draft; **Save** applies it to the selected server.
Cancel or Escape closes the editor without changing the draft. The settings
page has no raw JSON editor. Saving uses standard JSON serialization, preserving
quotes, backslashes and literal text. Rule values are compared as data and are
never evaluated as shell commands.

**Fill from current pane** samples the foreground process in the focused
terminal on this connection. It replaces the selected rule, or creates the
first rule when none exists; other rules remain unchanged. The executable name
becomes an `executable` / `exact` condition, and each sampled argument after
argv[0] becomes an `argument` / `exact` condition. All programs use the same
mapping, without interpreter-specific inference. PID and start time are not
recognition conditions. Review the filled conditions and remove parameters
that should not constrain future runs before applying. The existing limits
are eight conditions per rule and 512 bytes per value; samples exceeding those
limits report an error and preserve the draft. Sampling is asynchronous, and
results from a canceled popup or a changed focused pane are ignored.

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
`*` does not cross path separators. On macOS, foreground sampling uses sysctl
for the PTY process-group leader, preserves NUL-delimited argv tokens, and
rechecks process identity before accepting the result. Other platform paths use
bounded ps sampling whose whitespace parsing cannot reconstruct all quoting or
arguments containing spaces. Test recognition against the real target host;
do not assume identical argument fidelity across platforms.

Higher numeric priority wins when several enabled profiles match. Equal
priorities use the stable profile ID as the tie breaker. **Test recognition**
uses the focused terminal and the current unsaved draft, showing sampled PID,
command and arguments, all matching profiles and the selected rule. Testing
does not save the draft or change runtime classification.

## Status reports

**Status source** selects:

- `auto`: OSC reports when available, otherwise the process lifecycle.
- `osc`: Unknown until an OSC report is available; process exit still wins.
- `process`: Running/Exited lifecycle, ignoring OSC reports.

A process-only Running status indicates that the program exists. It does not
establish that the Agent is working on a task. OSC state 4 can mean a pause or
warning; it does not establish that approval is required.

Builtin profiles default to `osc`: without a report the state is Unknown, rather than inferring work from process existence. Explicit saved status-source overrides remain in effect.

Water supports OSC 7501 root program status and answers its `?` capability query before the following Device Attributes response. Reports map `idle` → Idle, `working` → Running, `blocked` → Paused, `done` → Completed and `error` → Error. The app identity must match the recognized Agent; these reports take priority over OSC 9;4. Clear and terminal reset withdraw the report. A foreground-run change invalidates prior status. Replay restores metadata without sending query replies. Messages are bounded and not displayed; job records are unsupported.

OSC 9;4 is progress metadata: 0 clears progress, 1 is determinate progress, 2 error, 3 indeterminate, and 4 paused. State 0 does not prove successful completion; state 4 does not prove permission approval. Water retains the latest report until updated or cleared.

Verified installed-version behavior: Claude Code 2.1.285 emits progress 3 during work and clears when resting, gated by its supported-terminal detection and `terminalProgressBarEnabled`. Codex 0.162.1 and OpenCode 1.18.34 do not emit built-in structured progress/lifecycle reports; their textual notifications are not parsed as state. Pi 1.2.0 supports negotiated OSC 7501 and separately optional OSC 9;4 (`terminal.showTerminalProgress`). Newer upstream versions may differ; do not infer installed behavior from upstream HEAD. Water does not install hooks or spoof another terminal identity.

**OSC states 0,1,2,3,4** contains five comma-separated presentation states. The
default is `Idle,Running,Error,Running,Paused`. Supported values are `Idle`,
`Running`, `Error`, `Paused`, `Completed`, and `Unknown`. Clear the field to use
the default mapping. No hooks are installed, and no output text is interpreted
as an Agent state event.

Agent-specific interrupt, exit and force-stop actions are unavailable in the GUI, CLI, TUI and server dispatcher. Ordinary terminal input and terminal/pane lifecycle actions retain their normal semantics. Legacy `interrupt` and `exit` profile fields remain accepted for configuration compatibility but have no Agent control effect.

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
```

`--terminal TERMINAL_UUID` can replace `--pane`. Omitting a target uses the
server's active pane. The standalone import/export file is an Agent configuration object, with
`overrides` and `custom` arrays. It is not an entire Water application config.
Definitions are limited to 128 custom profiles/overrides and 256 KiB of compact
configuration JSON. Unknown fields and invalid definitions are rejected. Import replaces the saved
Agent configuration. An empty object restores all builtin defaults and removes
custom definitions. In the application config, the same object lives under
`agents` (or `overrides.agents` for an override envelope).

The server advertises `agent-profiles/v1`. A server supporting recovery can use the compatibility/recovery flow before
these settings are used. Without recovery support, automatic recovery restart is
unavailable; arrange an explicit upgrade that accounts for its running tasks.
Arbitrary action scripts and custom state event transports are outside this
first implementation.
