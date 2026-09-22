# Water · Agent 工作约定

本文件约束代码修改、测试和发布；当前模块入口见 [ARCHITECTURE.md](ARCHITECTURE.md)，命令行和操作手册见 [docs/Documents.md](docs/Documents.md)。实现、测试和本文件冲突时，先核对当前代码与基线，不用历史文案替代验证。

## 工作边界

- 只推进用户当前任务，不顺手扩展文件浏览器、图片预览或结构化 Agent Surface。现有 coding-agent 能力（前台进程检测、绑定、状态和侧栏交互）属于既有范围。
- 修改前检查相关实现、测试和 `git status`；尊重用户限制的读取范围，并明确没有验证的结论。
- 保持 `Cargo.toml` 的 GPUI/gpui_platform revision pin；除非任务本身是依赖升级，不改 pin。
- 只保留一个大小写正确的 `AGENTS.md`；新增路径前检查 `git ls-files`，不要创建 `Agents.md`。

## 从提交历史固化的必须项

仓库维护提交使用过 `surtsingv` 和 `Tsingv` 两个 author identity。两组提交反复确认的约束统一如下；新实现不能为了方便回退这些边界：

- 外部模型修改必须走 `AppCommand → CommandDispatcher → model`。UI、`water ctl`、scenario 和远端控制只能通过 transport/channel 调用，不能直接改模型或引入全局 `Mutex<ApplicationModel>`。
- 服务端保留模型、PTY 元数据和有界原始 replay；客户端拥有终端模拟、窗口选择、滚动和拖拽预览。GPUI 主线程不得做阻塞 I/O、ANSI 解析或重型历史处理。
- PTY 的 `Output → Resize → Exit` 必须保持有序；attach 的有界 replay 要和 live 流完整衔接、按 sequence 去重，并在背压和关闭时有界。不得恢复逐屏服务端快照或无限输出推送。
- 实体、connection、operation 使用 typed ID 和完整 UUIDv4 随机身份。JSON/CLI 使用 UUID 字符串，二进制保留全部 16 字节；不能用集合下标、进程计数、截断或浮点数替代。命令必须路由到对象所属 connection。
- 控制入口是内置的 `water ctl`，不是独立 `waterctl`。GUI 验证必须走 Water 自己的 control API 和真实 action/model 路径；点击使用 control API 自己的 `ui click`，不使用 `xdotool`、`xte`、AppleScript 等系统注入。
- dev/release 必须完全隔离：socket、配置目录、Bundle ID、进程名、server 和远端 payload 都不能互用；启动 sibling/远端 server 前校验变体和协议，不覆盖仍在监听的 socket。
- 默认 GUI、tab、split 使用配置的真实 shell；空 workspace 入口只用于测试。配置项必须贯通默认值、override、合并/校验、Settings 读写和运行时投影。
- 默认日志结构化且安静；诊断按模块启用（例如 `RUST_LOG=water::pty=debug`），不默认打印逐 cell/逐 frame 日志。

## 验证与进程安全

- 运行与改动匹配的 fmt、clippy、单元/集成测试、scenario 和 GUI 检查；测试应在一分钟内完成。等待用 operation、event、revision、terminal output 或 process exit 的有界条件，不用固定 `sleep` 掩盖竞态、死锁或超时。
- GUI 交互使用 `water ctl`；headless 测试不能替代真实 GUI 验证。先检查当前机器的 display/GPU/X11/Wayland 环境，不硬编码 `DISPLAY` 或 `XAUTHORITY`。
- scenario shell 使用检测到的 zsh，优先 `/opt/homebrew/bin/zsh`；隔离启动文件的交互测试使用 `-f`。`--no-initial-terminal` 保留 workspace，`--empty-workspace` 从空模型开始。
- 测试实例必须使用唯一的 `WATER_CONTROL_SOCKET` 和临时 `WATER_CONFIG`/`--config`，记录本次 GUI/server PID。结束后及时关闭本次 socket 和 server，只清理已确认属于本次测试的 PID。
- 禁止 `pkill water`、`pkill -9 water`、`killall water`；进程名只能辅助辨认，不能单独作为清理依据。已有用户 GUI、server 和承载当前会话的终端不得终止。

## 构建与变体

- 只有 `dev` 和 `release` 两种运行身份。普通 `cargo build` 是优化后的 dev，`cargo build --release` 是 release；不要从 opt-level 推断身份。产物分别在 `target/debug` 和 `target/release`（指定 target 时再多一层 triple）。
- dev 使用 `water-dev` namespace：`/tmp/water-dev.sock`、`~/Library/Application Support/water-dev/config.json`、Bundle ID `dev.water.terminal.dev`、GUI/server 进程 `water-dev`/`water-srv-dev`。release 使用 `water` namespace：`/tmp/water.sock`、`~/Library/Application Support/water/config.json`、Bundle ID `dev.water.terminal`、进程 `water`/`water-server`。
- Linux→macOS 沿用仓库的 zig linker、framework stubs、平台 patch 和 Mac 预编译 Metal shader；交叉编译成功不等于 macOS 原生运行验证。构建脚本必须从本次变体的 payload 目录取 GUI/server，不能回退到任意已安装的 release server。

详细构建、测试、`water ctl`、scenario 和发布命令集中在 [docs/Documents.md](docs/Documents.md)。

## 签名与发布不可变规则

- `.github/workflows/macos-signed.yml` 是 signing-only workflow，唯一触发方式是 `workflow_dispatch`；不得把 Rust checkout、cargo、zig 或完整构建放回该 workflow。
- workflow 必须接收 `variant`、`publication`、`tag`、`source_release`、`source_asset`。release 只接受 `v*` tag，dev 只接受 `dev-*` tag；`prerelease` 只给 dev，`release` 只给正式版，`draft`/`none` 可用于两者。
- 固定顺序是：本地 `CODESIGN_SKIP=1` 构建 unsigned zip → `test -s`/`unzip -t` → 上传目标 tag 的 draft release → 触发 signing workflow。unsigned app 不提交 Git，也不能当作已交付安装包。
- 需要真实签名时显式设置 `CODESIGN_IDENTITY` 和 `CODESIGN_REQUIRED=1`；本地 ad-hoc/unsigned 产物只能作为构建或签名交接中间物。
- `SurTeamCode.p12` 只作为本地导入材料，放在仓库外并限制权限；`.env`、密码、p12、明文 identity 不得进入源码、workflow、日志或 shell tracing。Actions 只使用 `SURTEAM_CODE_P12_BASE64`、`SURTEAM_SIGN_PASS` 和可选的 `SURTEAM_SIGNING_IDENTITY`。
- Action 必须使用临时 keychain，签 nested GUI/server 和 app，并执行 `codesign --verify --deep --strict`；失败不得发布，`always()` 清理 keychain 和 p12。`CODESIGN_SKIP=1` 与 `CODESIGN_REQUIRED` 互斥。
