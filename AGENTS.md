# Water · Agent 工作约定

本文件规定如何修改和验证 Water；模块入口和当前实现见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 工作范围

- 按用户当前任务推进，不自行扩展文件浏览器、图片预览或结构化 Agent Surface。现有 coding-agent 功能包括进程检测、绑定及侧栏交互，不受旧 Phase 门禁限制。
- 修改前核对相关实现和当前基线；用户限制读取范围时遵守范围，并明确未经验证的结论。
- 保持 Cargo.toml 中的 GPUI pin；仅在任务涉及依赖升级时调整。

## 架构约束

- 外部应用模型命令走 `AppCommand → CommandDispatcher → model`，PTY 元数据和退出事件也由 dispatcher 在模型线程应用。UI、控制接口和场景通过 transport/channel 调用，不直接改模型，不引入全局 `Mutex<ApplicationModel>`。
- 区分服务端模型与客户端局部状态：窗口选择、拖拽预览、终端模拟和滚动不等于应用模型变更。GPUI 主线程不做阻塞 I/O、ANSI 解析或重型滚动历史处理。
- 服务端 PTY 输出走有界 replay 和有序事件流，客户端负责终端模拟；不能恢复服务端逐屏解析或逐输出推送模型快照。保持 Output/Resize/Exit 顺序及 replay 到 live 的完整衔接。
- 保持 typed ID 的稳定身份和随机分配，不用集合下标或进程内计数替代；运行时 ID 使用完整 UUIDv4，JSON/CLI 使用 UUID 字符串；不得截取或经浮点数转换。命令发往对象所属 connection，不能仅凭当前选中的连接路由。
- 默认 GUI、新 tab 和 split 都启动配置的真实 shell，保留空工作区测试入口。功能开关、主题、字体、快捷键及容量限制集中在 `AppConfig`。
- 新配置项贯通默认值、可选 overrides、合并/解析及运行时投影；设置面板同步读取、编辑、校验、渲染和生效方式。按改动验证整条链路。
- 默认日志保持结构化、安静；按模块开启诊断（如 `RUST_LOG=water::pty=debug`），不默认打印逐 cell/逐 frame 日志。

## 验证

- 运行与改动匹配的检查。除编译外，测试执行应在一分钟内完成；超时先排查等待、死锁或测试设计，不靠加 sleep/放宽超时掩盖问题。
- 用 operation 完成、事件、revision、输出或进程退出等有界等待同步，不用固定 sleep。历史“已通过”或“预存失败”需当前基线佐证，不能直接豁免失败。
- GUI 交互验证使用 `waterctl` / Water control API，不用 `xdotool`、`xte` 等系统注入工具。缺少必要交互时，在任务范围内补 control/automation 接口并走真实交互处理及应用命令路径。
- shell 场景使用检测到的 zsh，优先 `/opt/homebrew/bin/zsh`；隔离启动文件的交互测试用 `-f`。`--no-initial-terminal` 保留 workspace 但不建终端，`--empty-workspace` 从无 workspace 开始。
- headless 测试不能替代 GUI 验证；按实际机器检查显示环境，不假定 Linux 没有 X/GPU，也不硬编码 `DISPLAY` / `XAUTHORITY`。

## 测试实例与进程安全

Agent 可能依附于正在运行的 Water server；不要终止承载当前会话或用户终端的进程。

- 测试使用独立 `WATER_CONTROL_SOCKET` 和临时配置（`WATER_CONFIG` / `--config`），记录本次启动的 GUI/server PID。dev 默认隔离不能替代测试实例隔离。
- 清理只针对已确认属于本次测试的 PID，server 也需单独确认归属。禁止 `pkill water`、`pkill -9 water`、`killall water`；`pkill -x` 也不能隔离同名实例。
- 进程名仅用于辨认：dev GUI/server 为 `water-dev` / `water-srv-dev`，release 为 `water` / `water-server`，不能仅凭名字判断可安全终止。

## 构建与仓库

- 只分 dev 和 release：普通 `cargo build` 即优化后的 dev，产物在 `target/debug`；release 用 `--release`，产物在 `target/release`。指定 target 时多一层 `<triple>`。不再使用 dev-opt；打包默认 dev，显式 `WATER_APP_VARIANT=release` 才发布 release。
- dev 版必须完全独立：socket `/tmp/water-dev.sock`；配置 `~/Library/Application Support/water-dev/config.json`；Bundle ID `dev.water.terminal.dev`；进程 `water-dev` / `water-srv-dev`。release 使用 `/tmp/water.sock`、`~/Library/Application Support/water/config.json`、`dev.water.terminal`、`water` / `water-server`。
- 发布 dev 版前确认包内 GUI/server、远端 payload 和运行时身份均符合隔离要求；构建时必须校验 payload 变体，不能回退到任意已安装的 release server。使用以下构建命令和时间戳 tag；创建 release 后按发布任务上传本次产物，不能把空 release 当成已交付安装包。

  ```bash
  WATER_APP_VARIANT=dev bash scripts/build-macos-app.sh
  TAG="dev-$(date +%Y%m%d-%H%M)"
  gh release create "$TAG"
  ```

- Linux → macOS 构建沿用现有 zig linker、framework stubs 和平台 patch；Metal shader 在 Mac 预编译并维护仓库产物。入口见架构文档，交叉构建成功不等于 macOS 运行验证。
- 新增路径先查 `git ls-files` 的大小写冲突；只保留一个 `AGENTS.md`，不要再创建 `Agents.md`。本地 `.pi/` 会话目录应被忽略，不提交会话数据。
- Python 优先 `~/.venv/bin/python` / `~/.venv/bin/pip`；Node 工具先加载 fnm 环境并遵循项目指定版本。
