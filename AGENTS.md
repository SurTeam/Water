# AGENTS

## 项目规则

- Phase order is strict: finish and validate one phase before starting the next. Phases 1 and 2 are accepted; the current target is Phase 3 terminal UX/performance. Normal GUI startup opens one real-shell terminal; new tabs and split panes must also open real-shell terminals. GUI shortcuts are `Cmd-\\` for a right-side split, `Cmd--` for a downward split, and `Cmd-T` for a new terminal tab. Use `--no-initial-terminal` for a workspace-without-terminal baseline or `--empty-workspace` for workspace-creation smoke scenarios. Do not add file browser or image behavior. Coding-agent work is limited to read-only detection and the sidebar Agents section (the binding contract in `ARCHITECTURE.md`); do not add structured Agent Surfaces until Phase 5.
- State mutation has one path: `AppCommand` -> `CommandDispatcher` -> model. UI, `waterctl`, and scenarios use that path; they must not mutate model fields directly.
- IDs are typed, stable newtypes. Never use a `Vec` index as identity.
- `CommandDispatcher` owns the model on the model thread. Control requests cross a channel; do not introduce a global `Mutex<ApplicationModel>`.
- Operations are observable and waitable. Tests and scenarios synchronize with operation completion or explicit wait primitives, never fixed sleeps.
- Keep GPUI pinned to the revision recorded in `ARCHITECTURE.md` and `Cargo.toml`; do not upgrade it mid-phase.
- UI is a projection of revisioned snapshots. Keep blocking I/O and model mutation off the GPUI thread; UI command handlers dispatch through a detached GPUI task and background executor.
- Terminal mutable state belongs to a dedicated PTY worker. The model stores terminal metadata and revisioned visible snapshots; `TerminalRegistry` is a narrow synchronized query/wait surface, not application-state ownership.
- Real-shell validation uses the detected zsh, preferring `/opt/homebrew/bin/zsh`; `tests/scenarios/terminal_zsh.json` runs zsh interactively with `-f` and waits on output/process state.
- Application defaults live separately in `AppConfig` and are loaded from `~/Library/Application Support/water/config.json` on macOS, or from `WATER_CONFIG`/`--config`. Keep feature toggles, theme colors, font metrics, and terminal scrollback limits out of hard-coded UI/worker paths.
- Default logs are structured and quiet. Use `RUST_LOG=water::pty=debug` (or another module) for focused diagnostics; never add per-cell/per-frame logs by default.
- Testing should definitely not take a long time. Aside from the compilation process, all other tests should be completed within one minute. If they take longer than that, we need to investigate the cause and fix the issue.

## 进程管理（重要）

Agent 运行在 water-server 进程内部。**永远不要 `pkill water`、`pkill -9 water`、`killall water`**——这会把你自己也杀了。

### 进程命名规则

| 构建 | 进程 | comm 名称 |
|------|------|-----------|
| debug / dev-opt | GUI | `water-dev` |
| debug / dev-opt | Server | `water-srv-dev` |
| release | GUI | `water` |
| release | Server | `water-server` |

### 杀 dev/test 进程

```bash
# 杀 dev GUI
pkill -x water-dev

# 杀 dev server
pkill -x water-srv-dev

# 杀 release GUI（注意：不会匹配 water-server）
pkill -x water

# 绝对不要
pkill water          # ❌ 会匹配 water-server
pkill -9 water       # ❌ 同上
killall water        # ❌ 同上
```

### 启动测试实例

```bash
# 始终用独立的 socket 路径，避免碰到生产 server
WATER_CONTROL_SOCKET=/tmp/water-test-$RANDOM.sock ./target/debug/water &
TEST_PID=$!
# ... 测试 ...
kill $TEST_PID       # GUI
pkill -x water-srv-dev  # 如果启动了 embedded server
```

### 测试 release

```bash
# 同样用独立 socket
WATER_CONTROL_SOCKET=/tmp/water-test-$RANDOM.sock ./target/release/water &
TEST_PID=$!
# ... 测试 ...
kill $TEST_PID       # 按 PID 杀，不用 pkill
# 如果有 embedded server，按 PID 杀或 pkill -x water-server
# 但要先确认生产 server 不在跑，或用 pgrep 确认
```

### 绝对禁止

- `pkill -9 water`
- `killall water`
- `pkill water`（不带 `-x`）
- 任何可能匹配到 `water-server` 的 kill 命令

### 安全做法

- `pkill -x water-dev`（debug/dev-opt GUI）
- `pkill -x water-srv-dev`（debug/dev-opt server）
- 按 PID kill（`kill $PID`）
- `pgrep -x water` 先确认目标再 kill