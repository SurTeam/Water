# 控制命令与自动化

`water ctl` 是内置的控制入口，与 GUI 共用真实命令和动作路径。自动化必须通过所属 connection 操作对象，用有界状态条件等待完成，并只清理自身实例。GUI 自动化遵守 [测试策略](testing.md)。

## `water ctl`
The control client is built into the `water` binary; the old standalone `waterctl` binary no longer exists. Prefer the explicit `ctl` namespace:

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ping
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock info
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock server info
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock connections list
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock state
```

The socket resolves from `--socket`, `WATER_CONTROL_SOCKET`, `server.socket_path`, `startup.control_socket`, and then the dev/release default. Bare aliases such as `water state` and `water ui key cmd-t` remain available, but `water ctl` makes scripts unambiguous.

### Model and terminal commands

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock workspace new
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock tab new
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock pane split --right
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock pane content \
  --pane PANE_ID --row 0 --rows 8 --column 0 --columns 120
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock terminal snapshot \
  --terminal TERMINAL_ID
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock debug memory
```

Mutating commands return an operation snapshot. Synchronize on the returned operation, terminal output, revision, or process exit; do not add arbitrary sleeps to make a test pass.

### Real GUI control

`ui key` dispatches a keystroke through the GUI's input handler. `ui click` dispatches a control-API click at a window-local point, `ui wheel` sends a wheel event, `ui state` reports the current window state, and `ui screenshot` captures the real Ebitengine rendered frame.

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui key cmd-t
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui click \
  --x 240 --y 100 --click-count 2
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui state
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui screenshot \
  --output /tmp/water-window.png
```

Use Water's control API for GUI checks. Do not use `xdotool`, `xte`, AppleScript keystrokes, or native desktop coordinate injection. The control screenshot is a Water-window capture, not a desktop-wide screenshot; platform support must be verified on the machine running the check.

### Scenarios

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/workspace_basic.json
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/terminal_basic.json
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/terminal_zsh.json
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/agent_detection.json
```

Scenarios are expected to use bounded operation/output/exit waits. Run them against a dedicated socket and temporary config, then shut down only the server created for that run.
