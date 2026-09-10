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

## 跨平台构建（Linux → macOS）

- macOS arm64 交叉编译依赖 zig cc 作为 linker（`scripts/zig-cc-mac`），系统 `ld64.lld` 有 tbd 解析 bug。
- Framework stub（`scripts/stub/macos-sdk/`）用 `symbols: ['*']` 通配符，所有 ObjC/framework 符号留 undefined，靠 `-Wl,-undefined -Wl,dynamic_lookup` 在运行时解析。
- `shaders.metallib` 必须在 Mac 上预编译（`scripts/build-metallib.sh`），产物提交到 `scripts/prebuilt/`。Linux 上无法编译 Metal shader。
- `gpui_apple` 和 `media` crate 通过 `[patch]` 指向 `scripts/stub-gpui_apple/` 和 `scripts/stub-media/`，在 macOS host 上行为与上游一致，在非 macOS host 上提供 fallback（空 metallib / stub bindings）。
- 新增 macOS-only 的 framework 依赖时，需要在 `scripts/stub/macos-sdk/` 加对应的 `.tbd` stub。
- dev 变体用 `--profile dev-opt`（opt-level 3，inherits release），不是 `debug`。`WATER_BUILD_PROFILE` 由 build.rs 从 cargo `PROFILE` 环境变量烘焙，所有 dev/release 判断都用 `!= "release"`。

## 文件系统与 Git

- **文件名大小写敏感**：macOS 默认文件系统不区分大小写，Linux 区分。不要在仓库里同时存在 `Agents.md` 和 `AGENTS.md`（已发生过）。新增文件前 `git ls-files | grep -i <name>` 检查。
- `.gitignore` 里加 `.pi/`（pi agent 会话目录）。

## 配置与主题

- 新增 `ThemeColors` 字段时，必须同步更新：`ThemeConfig` 结构体 + `Default` impl + `ThemeConfigOverrides` + `apply_to` + `colors()` 方法 + 测试里的 `ThemeColors` 构造。
- 新增 `UiConfig` / `TerminalConfig` 字段时，同步更新对应的 `Overrides` 结构体和 config 解析（`apply_to`）。
- 设置面板新增字段需要：`SettingField` 枚举 + `id()` + `is_color()`（如适用）+ `raw_value()` + `apply_edit()` + `color_value()`（如适用）+ render 列表。

## 测试

- 3 个 dialog 测试（`dialog_input_caret_editing`、`rename_dialog_scopes`、`text_input_dialog_confirm`）是预存失败（workspace ID collision），不是回归。判断是否引入新失败时先看 baseline。
- `cargo test` 在 Linux 上跑的是 headless 测试（无 GPU），但 **X 服务器是有的**（X11，LXQt 桌面）。需要 GUI 验证时：
  ```bash
  export DISPLAY=:0
  export XAUTHORITY=$(ls -t /tmp/xauth_* | head -1)  # 每次 SDDM 登录会变
  ./target/debug/water &
  ```
  截图工具：`xwd -root -silent | convert xwd:- out.png`（ImageMagick）。
- macOS 上无 X，用 `screencapture` 或 GPUI 自带截图。
- **GUI 交互测试必须使用 Water 提供的操作接口**（`waterctl` / control socket API），如点击、滚动、按键等。不要用 `xdotool`、`xte` 等系统工具直接操作窗口。如果现有接口不支持某个操作（如滚动到指定位置、双击、拖拽），应该先在 Water 的 control/automation 层添加对应接口，再用新接口测试。这保证测试走的是和真实用户一致的状态变更路径（`AppCommand` → `CommandDispatcher` → model），而不是绕过应用直接发 X 事件。