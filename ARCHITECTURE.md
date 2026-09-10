# Water 架构

执行约定见 [AGENTS.md](AGENTS.md)。本文于 2026-09-11 对照代码、Cargo.toml 和构建脚本整理；描述当前实现与边界；具体检查结果随对应任务记录。

## 从哪里开始

| 工作 | 入口 |
|---|---|
| 应用状态、命令及完成等待 | [app/runtime.rs](src/app/runtime.rs)、[command/dispatcher.rs](src/command/dispatcher.rs)、[command/operation.rs](src/command/operation.rs) |
| workspace/tab/pane 拓扑与身份 | [app/model.rs](src/app/model.rs)、[pane/model.rs](src/pane/model.rs)、[ids.rs](src/ids.rs) |
| PTY、回放及订阅 | [terminal/worker.rs](src/terminal/worker.rs)、[terminal/model.rs](src/terminal/model.rs)、[terminal/replay.rs](src/terminal/replay.rs)、[terminal/stream.rs](src/terminal/stream.rs) |
| 客户端终端模拟与窗口交互 | [terminal/emulator.rs](src/terminal/emulator.rs)、[ui/application.rs](src/ui/application.rs)、[ui/workspace.rs](src/ui/workspace.rs) |
| 协议、CLI 与 GUI 自动化 | [control/protocol.rs](src/control/protocol.rs)、[control/server.rs](src/control/server.rs)、[control/client.rs](src/control/client.rs)、[bin/waterctl.rs](src/bin/waterctl.rs)、[ui/control.rs](src/ui/control.rs) |
| 启动、配置、Agent 检测 | [main.rs](src/main.rs)、[server.rs](src/server.rs)、[config.rs](src/config.rs)、[agent/model.rs](src/agent/model.rs) |
| 打包与 SSH 部署 | [build-macos-app.sh](scripts/build-macos-app.sh)、[build-embedded-servers.sh](scripts/build-embedded-servers.sh)、[build.rs](build.rs)、[remote.rs](src/remote.rs) |

## 状态归属

GUI 先连接已有 server；没有可用 server 时，根据配置启动 detached server 或进程内 embedded server。只有 detached/远端 server 可以独立于 GUI 存活，不能把所有启动模式都描述成独立进程。

| 状态 | 所有者 |
|---|---|
| 应用拓扑、terminal/session 元数据 | 模型线程中的 CommandDispatcher / ApplicationModel |
| PTY I/O、子进程、原始输出事件 | 服务端 PTY worker |
| 有界 replay、生命周期查询和订阅入口 | TerminalRegistry / ReplayRing，使用局部同步，不拥有应用模型 |
| Alacritty Term/Processor、屏幕和 scrollback | 客户端每个已附着终端的 emulator worker |
| 窗口选择、IME、拖拽预览、不可变终端投影 | GPUI 客户端及各 WorkspaceView |

外部命令与终端数据分成两条路径：

```text
UI / waterctl / scenario
  → 对象所属 connection 的 CommandTransport
  → CommandDispatcher → ApplicationModel
  → operation / event / revisioned metadata snapshot

PTY → replay ring + 有序 Output / Resize / Exit
  → 客户端 emulator worker → 不可变 TerminalSnapshot → GPUI
```

模型线程还通过 dispatcher 应用 PTY 的进程元数据、标题和退出通知。模型变化才发布 metadata snapshot，持续输出不逐屏推送模型。快照邮箱只保留最新待消费版本；操作等待及终端输出/退出等待不阻塞 GPUI，终端等待由独立任务查询 registry。

服务端不维护持续运行的屏幕模拟器。需要 cells 的查询调用方可通过 `snapshot_from_replay` 临时重放；这与 GUI 的长期 emulator、服务端 metadata snapshot 是三件不同的事。GUI 通过 worker command 执行本地滚动，键盘输入和终端应答仍通过所属连接送往 PTY。

## 回放、连接与身份

终端事件有单调 sequence，Output、Resize、Exit 在同一流中。attach 返回有界历史和 live tail，重复事件按 sequence 跳过；回放阶段抑制终端查询等副作用，进入 live 后恢复。原始 replay 与客户端 scrollback 是不同存储，不能把旧服务端 grid 的限制直接当成现行内存预算。

每个连接持有独立 transport 和模型投影，客户端终端状态也按连接管理。窗口选择及拖拽预览可以局部变化，提交应用状态时必须保留对象的连接归属。

`ids.rs` 中的实体、连接和操作 ID 是完整 UUID 的 typed newtype；IdAllocator 保留 UUIDv4 的全部 128 位。JSON 和 CLI 使用标准 UUID 字符串，二进制流保留全部 16 字节，避免数字精度丢失。反序列化仍接受旧场景的 u64 fixture，但新生成的 ID 不截取；不能以随机唯一性为由省略连接归属。PaneNode 表达分屏拓扑，pane 引用 surface，terminal 与逻辑 session 分别有自己的身份。

## 控制与 Agent 绑定

本地使用 Unix socket；SSH 使用 OpenSSH 转发同一控制协议，后台完成部署及就绪等待。当前协议 v4 的控制消息和 attach replay 使用 JSON，live 终端事件使用二进制帧；ID 字段扩为 16 字节。请求携带 build_variant，命令、session.open 和 shutdown 均要求协议与 dev/release 变体匹配，旧协议需使用对应旧客户端。版本与编码定义集中在 control/protocol.rs。

GUI 控制目前提供 keystroke、wheel、snapshot 和 screenshot 请求；screenshot 需要 `runtime-screenshot` feature。它们经 UI channel 到 GPUI 的交互处理路径，不意味着已经有通用 click/drag 自动化接口。窗口局部操作不必产生 AppCommand；涉及应用模型时才派发对应命令。

Agent 检测基于前台进程名及 argv，由模型派生绑定，侧栏和状态查询消费同一 workspace/tab/pane/terminal 路径。退出时清除绑定；类型切换产生 started/stopped 事件，只有活动标志改变不产生该事件。PTY 活跃只说明近期输出，不代表语义上的“工具运行”或“等待输入”。

## 配置与构建

AppConfig 合并默认值和可选 overrides。显式 `--config` / `WATER_CONFIG` 可选路径；macOS 默认先选原生 Application Support 路径，原生文件不存在时可选已有 XDG 风格配置。dev/release 各用自己的目录名，不能把原生路径写成唯一加载位置。

Cargo.toml 的 GPUI 和 gpui_platform 固定到同一 revision；平台 patch 指向 stub-media 和 stub-gpui_apple。只有 dev/release 两种构建：dev 默认 opt-level 3、thin LTO、单 codegen unit，保留行表调试信息；release 同样优化。build.rs 将 Cargo 的 debug/release profile 归一为 dev/release 身份，不能根据优化级别推断身份。

dev 的具体 socket、配置、Bundle ID、进程名和发布命令见 [AGENTS.md](AGENTS.md#构建与仓库)。隔离贯通以下路径：

- macOS/Linux 打包和四种远端 server payload 使用同一变体；缓存放在 `target/embedded-servers/<variant>`，build.rs 检查变体标记后才嵌入。原生包从本次实际 build 目录取 GUI 和 server。
- dev 默认配置与 zsh integration 使用 water-dev 目录。启动 sibling server 前通过 `--build-variant` 校验；服务端不会替换仍在监听的 socket。
- SSH 转发/master 身份包含变体，远端 socket、缓存目录和日志也区分 water-dev/water。server.info 返回构建身份，远端二进制启动前同样校验变体；缺少 payload 时不回退启动任意已安装的 server。
- macOS 保留独立 Water Dev.app / Water.app；Linux 归档区分 water-dev / water，并包含 server 和 CLI，打包不会清空另一个变体的 dist 产物。

Linux → macOS 构建使用 scripts/zig-cc-mac、scripts/stub/macos-sdk 下的 framework stubs 和平台 patch。Metal shader 通过 scripts/build-metallib.sh 在 Mac 预编译，保存在 scripts/prebuilt；跨平台编译不能替代原生运行检查。

## 验证入口

[automation/scenario.rs](src/automation/scenario.rs) 维护场景执行和等待；[tests/hotpath.rs](tests/hotpath.rs) 覆盖持续输出不触发 state dump / snapshot push，[tests/resize_stream.rs](tests/resize_stream.rs) 覆盖 resize 顺序及 detach/reattach。[tests/protocol.rs](tests/protocol.rs) 验证跨变体请求拒绝与 socket 所有权；[tests/build_variants.rs](tests/build_variants.rs) 用隔离的模拟编译器执行真实打包脚本，检查 dev/release GUI、server、payload 及产物共存。模拟打包不替代真实跨平台编译和 GUI 运行验证。

遵守 AGENTS 的测试时限、控制接口和实例隔离要求。实现修改按风险运行相关测试和 GUI/打包验证，不继承历史“全通过”或“永久失败”结论。
