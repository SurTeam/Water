---
name: water-control
description: Drive a running Water terminal GUI/server from the CLI via `water ctl`. Use when verifying GUI interactions, automating workspaces/tabs/panes/terminals, taking screenshots, or running scenario tests against Water. Triggers: "verify in Water GUI", "take a Water screenshot", "split a pane in the running Water", "run a Water scenario", "check terminal output in Water".
---

# Water Control Interface

Drive the running Water GUI/server through its control socket. All GUI
interaction verification uses this interface (never xdotool/xte or other
system input injection).

The CLI is `water ctl <command>`; every command also works bare
(`water state`, `water ui key cmd-t`, ...). There is no separate `waterctl`
binary — it was merged into `water ctl`.

## Socket resolution

`--socket PATH` (or `--socket=PATH`) > `WATER_CONTROL_SOCKET` env >
`server.socket_path` (config) > `startup.control_socket` (config) > platform
default:

- dev: `/tmp/water-dev.sock`
- release: `/tmp/water.sock`

Isolated test instances must use their own `WATER_CONTROL_SOCKET` plus
`WATER_CONFIG` / `--config` pointing at a temp config, and record the PIDs
they started.

## Command reference

| Area | Commands |
|------|----------|
| State | `state` — full workspaces/tabs/panes dump (JSON) |
| UI | `ui key <keystroke>` (e.g. `cmd-t`), `ui state`, `ui screenshot [--output FILE]`, `ui wheel --x --y --dx --dy` |
| Server | `server info`, `server shutdown` |
| Debug | `debug memory`, `debug metrics` |
| Workspace | `new` / `list` / `activate ID` / `rename [--workspace ID] --title T` / `reorder --workspace ID --index N` / `close [--workspace ID]` |
| Tab | `new [--title T]`, `rename [--tab ID] --title T`, `close [--tab ID]`, `activate [--tab ID \| --index N]` |
| Pane | `split --left\|--right\|--up\|--down [--pane ID]`, `close [--pane ID]`, `focus [--pane ID \| --left ...]`, `resize --ratio F [--pane ID]`, `resize-split --tab ID --path 0,1 --ratio F`, `rename-agent --pane ID --label L`, `move-to-workspace --pane ID --workspace ID`, `input --pane ID --text T`, `content --pane ID [--row R --rows N --column C --columns M]` |
| Surface | `replace --pane ID --empty` |
| Terminal | `spawn [--pane ID] [--program P] [--columns C --lines L]`, `send [--terminal ID \| --pane ID] <text>`, `send-bytes --hex HEX`, `resize --columns C --lines L`, `scroll --lines N`, `contains --terminal ID <text> [--timeout-ms N]`, `wait-exit --terminal ID [--timeout-ms N]`, `snapshot --terminal ID` |
| Operation | `get ID`, `wait ID` |
| Scenario | `scenario run PATH` (JSON scenario files) |
| Misc | `ping` |

Dispatch commands (`workspace new`, `tab close`, `pane split`, `terminal
send`, ...) print the awaited operation result as JSON; exit non-zero with
the error message when the operation fails.

IDs are numeric in the CLI (typed full UUIDs internally). Discover them from
`state` — workspace, tab, pane, and terminal IDs all appear in the dump.

## Verification patterns

### Start an isolated instance and verify

```sh
export WATER_CONTROL_SOCKET=/tmp/water-verify-$$-sock
export WATER_CONFIG=$(mktemp -d)/config.json
./target/debug/water --empty-workspace &   # or --no-initial-terminal
```

Use the dev build (`target/debug/water`, socket `/tmp/water-dev.sock`) for
running dev GUIs; do not kill the user's running instances — verify PID
ownership before any cleanup, and never `pkill water`.

### Terminal round-trip

```sh
water ctl pane content --pane <id> --columns 80          # read screen
water ctl pane input --pane <id> --text 'printf hello\n' # send to shell
water ctl terminal spawn --pane <id> --program /opt/homebrew/bin/zsh
water ctl terminal wait-exit --terminal <id> --timeout-ms 5000
```

### GUI interaction (real keystroke path)

```sh
water ctl ui key cmd-t                 # dispatch through focused window/action tree
water ctl ui state                     # client-side UI snapshot
water ctl ui screenshot --output /tmp/water.png
```

### Wait for outcomes

Prefer bounded waits over fixed sleeps: `terminal contains --terminal <id>
"prompt"`, `terminal wait-exit --terminal <id>`, `operation wait <id>`.

## Scenarios

`water ctl scenario run tests/scenarios/<name>.json` runs a recorded JSON
scenario (dispatch + waits + assertions) against the socket; see
`tests/scenarios/README.md` for the format. Prefer adding scenario steps to
existing files over one-off ad-hoc scripts when the interaction is
repeatable.