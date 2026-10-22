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

Model dispatch commands return an operation snapshot. `pane execute` is a synchronous RPC and returns its execution result directly, without an operation ID. Synchronize on the returned operation, terminal output, revision, or process exit; do not add arbitrary sleeps to make a test pass.

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

## 在指定 pane 执行命令

```sh
water ctl server info
water ctl pane execute --pane PANE_UUID \
  --command 'printf "hello\n"; exit 7' --timeout-ms 30000
```

先检查目标 connection 的 `server info` 是否含 `pane-execute/v1` capability。旧的 detached/remote server 可能继续运行旧代码；升级 GUI 不会自动替换它，也不得为获得此接口擅自重启用户 server。

RPC 为 `pane.execute`，参数是 `pane_id`、`command`、`timeout_ms`、`max_rows`、`max_bytes`。必须显式指定终端 pane，且处于空闲交互式 `sh`、`bash`、`dash`、`ksh` 或 `zsh` 的空输入行。只能检测前台进程组，无法证明输入行为空；调用方须先检查目标，执行期间避免并发输入。每个终端仅允许一个 execute，其他连接的模型命令不受执行等待阻塞。

命令通过当前 pane 的交互 shell 启动前台 `/bin/sh` 子进程，继承当前工作目录与导出环境；不承诺父 shell 的 alias/function，`cd` 和变量赋值不会持久化到交互 shell。传输依赖系统 `base64 -d`，命令通过编码传入以避免 PTY 控制字符和 canonical 长行限制。

| 参数 | 默认与范围 |
| --- | --- |
| `command` | 1–16384 bytes，不含 NUL |
| `timeout_ms` | 默认 30000；1–600000；RPC 的 0 使用默认值，CLI 显式 0 无效 |
| `max_rows` / `--max-rows` | 默认及上限 256，最小 1；0 使用默认值 |
| `max_bytes` / `--max-bytes` | 默认及上限 1048576，最小 1；0 使用默认值 |

响应包含 `pane_id`、`terminal_id`、`status`、`text`、`lines`、可选 `exit_code`、`timed_out`、`signals`、`truncated`、`alt_screen` 和失败时的 `error`。`completed` 表示命令已结束，`exit_code` 仍可能非零；`timed_out` 表示超时；`failed` 表示执行或清理失败，仍尽量携带屏幕文本。CLI 对 `completed`（包括非零命令退出码）和 `timed_out` 返回 0，仅 `failed` 或 RPC/参数错误返回非零。Agent 必须同时检查状态、超时与退出码。

输出来自保留 replay 和本次有序 PTY 流构建的临时终端模拟器，通过唯一 OSC 标记记录开始/结束行，在范围有效时提取其中的最终屏幕文本，排除输入回显。它不是 stdout/stderr 字节日志，也不是 GUI viewport，不应用 GUI 的 Agent clear policy。重绘覆盖或历史裁剪的内容无法恢复；行/字节上限、resize、备用屏幕和部分光标定位会触发截断或退化为有界活动屏幕，检查 `truncated`、`alt_screen`；退化屏幕可能包含执行前内容或输入回显。若模拟器读取本身超过 1 MiB cell-text 预算，可能返回 `failed` 且没有文本，而不是截断结果。标记用于同步，不是对恶意终端程序的安全边界。

超时先校验本次包装进程的 PID、PGID、开始身份和前台所有权，再向其进程组发送 SIGTERM，250ms 后必要时 SIGKILL；绝不把 pane shell 当作取消目标。包装进程收到 TERM 后保留到宽限期结束以便清理忽略 TERM 的后代，因此通常会记录两种信号，不能据此判断用户命令是否忽略 TERM。执行计时从 replay 重建之后开始，覆盖命令提交与运行；清理最多额外等待约 2s，CLI 给 RPC 额外 5s，因此该时限不保证整个请求的严格墙钟耗时。后台或脱离进程组的后代、SSH 远端任务不保证终止；身份不明时拒绝发信号并报告清理失败。调用方断开连接不构成取消请求，服务端仍受原执行时限约束。
