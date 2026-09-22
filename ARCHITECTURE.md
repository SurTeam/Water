# Water 架构

本文描述当前实现的边界和入口；工作约定见 [AGENTS.md](AGENTS.md)，运行、测试和打包命令见 [docs/Documents.md](docs/Documents.md)。不要用旧的 Phase 叙述推断模块职责。

## 入口地图

| 关注点 | 入口 |
| --- | --- |
| 应用模型、命令、operation、revision/event | [`src/app/`](src/app/)、[`src/command/`](src/command/)、[`src/event/mod.rs`](src/event/mod.rs) |
| workspace/tab/pane 拓扑和 typed ID | [`src/workspace/`](src/workspace/)、[`src/pane/model.rs`](src/pane/model.rs)、[`src/ids.rs`](src/ids.rs) |
| PTY、元数据、回放和终端流 | [`src/server.rs`](src/server.rs)、[`src/terminal/model.rs`](src/terminal/model.rs)、[`src/terminal/worker.rs`](src/terminal/worker.rs)、[`src/terminal/replay.rs`](src/terminal/replay.rs)、[`src/terminal/stream.rs`](src/terminal/stream.rs) |
| 客户端模拟、滚动、选择和绘制 | [`src/terminal/emulator.rs`](src/terminal/emulator.rs)、[`src/ui/application.rs`](src/ui/application.rs)、[`src/ui/workspace.rs`](src/ui/workspace.rs) |
| 控制协议、CLI 和 UI automation | [`src/control/`](src/control/)、[`src/ctl/mod.rs`](src/ctl/mod.rs)、[`src/ui/control.rs`](src/ui/control.rs)、[`src/automation/`](src/automation/) |
| 配置、启动和构建身份 | [`src/config.rs`](src/config.rs)、[`src/main.rs`](src/main.rs)、[`build.rs`](build.rs)、[`src/lib.rs`](src/lib.rs) |
| SSH 连接、远端 server 和 agent 检测 | [`src/remote.rs`](src/remote.rs)、[`src/agent/model.rs`](src/agent/model.rs)、[`src/control/protocol.rs`](src/control/protocol.rs) |

## 运行时分层

```text
GUI / water ctl / scenario / remote client
        │  对象所属 connection 的 CommandTransport
        ▼
CommandDispatcher → ApplicationModel
        │
        ├─ operation / state revision / bounded event history
        └─ PTY 元数据、标题、agent 绑定和退出事件

PTY worker → ordered Output / Resize / Exit
          → bounded replay ring + live stream
          → client emulator worker → immutable TerminalSnapshot → GPUI
```

`ApplicationModel` 只在线程内由 dispatcher 修改。control server/client、GUI 和 scenario 是 transport 适配层；它们不能通过共享全局模型绕过命令路径。持续终端输出不逐屏写入模型 snapshot，模型 snapshot 也不能替代终端流。

## 状态归属

| 状态 | 所有者 | 备注 |
| --- | --- | --- |
| workspace、tab、pane tree、surface/session/terminal 元数据 | server 的 model thread | 通过 command、operation、revision/event 对外提供 |
| PTY 子进程、原始字节、进程名、resize/exit | server PTY worker/registry | 不在 GPUI 主线程执行阻塞 I/O |
| replay 边界、attach、live 订阅 | server `TerminalRegistry`/`ReplayRing` | 有界、按 sequence 衔接 |
| Alacritty emulator、屏幕、scrollback、selection、viewport | 每个 connection 的 client worker/UI | 不等于应用模型变更 |
| active window、焦点、拖拽预览、tab-strip 滚动 | GPUI client | 提交拓扑变化时才发送应用命令 |
| host/workspace/agent 的显示排序与侧栏颜色 | workspace UI/config | 不能改变对象的 connection 归属 |

## 终端生命周期与内存边界

- PTY 的 `Output`、`Resize`、`Exit` 共用有序事件序列。attach 返回有限 replay 和 live tail；客户端按 sequence 去重，不能在 replay/live 交接处丢事件或重复应用。
- 服务端不维持长期运行的屏幕模拟器。需要 cells 的查询可以从有限 replay 临时重放；GUI 的长期 emulator、服务端 metadata snapshot 和查询投影是三个不同概念。
- replay 原始字节、GUI scrollback grid 和 recent-output 查询各有边界。焦点终端可使用配置的滚动历史预算，失焦终端压缩到 inactive 上限，多个终端共享总字节上限；不能把服务端 replay 上限误当成客户端 scrollback 上限。
- PTY reader 在背压、空唤醒、关闭和 detach 时必须可停止且有界；关闭 tab/pane 后应断开终端订阅，退出 terminal 的 snapshot/query 仍要能被有界 observer 读取。

## Connection、远端和身份

每个 Local/Remote connection 有自己的 control transport、model projection 和 client terminal state。SSH 使用 OpenSSH ControlMaster 与 Unix socket forward；首次连接按远端 OS/CPU 选择匹配的 embedded server payload，缓存和 socket 按版本、协议、namespace、destination 隔离。兼容 server 可复用，不兼容 server 不能被静默替换；transport 断开时侧栏保留 offline/dimmed host，重新连接不应丢失其服务端 workspace。

`src/ids.rs` 的 `WorkspaceId`、`TabId`、`PaneId`、`SurfaceId`、`TerminalId`、`SessionId`、`ConnectionId` 和 `OperationId` 是 typed newtype，新的身份使用完整 UUIDv4。JSON/CLI 使用标准 UUID 字符串，二进制 terminal frame 保留 16 字节；旧数值 fixture 只为兼容反序列化，不能成为新实现的身份策略。

## UI、控制和 coding agent

`water ctl` 已并入 `water` binary；协议当前为 v4，control message/attach replay 使用 JSON，live terminal event 使用带完整 ID 的二进制 frame，API signature 为 `water-control/v4`。`water ctl info`、`server info` 和 `connections list` 用于检查 client/server/build compatibility。UI automation 目前覆盖真实 keystroke、click、wheel、snapshot 和（启用且平台支持时的）window screenshot；它不提供通用 drag/pointer stream，测试不能绕过 control API 使用系统注入。

agent 检测依据 terminal 的前台进程和 argv，归一化为 `AgentKind`/session label，再沿同一 workspace/tab/pane/terminal 路径进入 model projection、事件和侧栏。当前范围是检测、绑定、状态、排序和侧栏交互，不把它扩展成新的结构化 Agent Surface。近期输出活跃只表示有未提交本地输入回显之外的输出，不等价于语义上的“等待输入”或“工具运行”。

终端 UI 还负责 selection/copy、双击边界、OSC 8 hyperlink、本地/远端下载确认、scrollback viewport、tab/workspace 导航和非焦点 pane dimming；这些局部行为不能绕过命令/connection 边界改变服务端模型。

## 配置与构建身份

`AppConfig` 由内置 defaults 与可选 override 合并而成；startup、server、shell、terminal、theme、UI、shortcut、feature 和容量限制必须同时在解析、Settings 读写、校验和运行时投影中保持一致。窗口退出、restart-required 配置和已有 PTY 的生命周期不能被静默混淆。

项目只有 dev/release 两种运行身份。`build.rs`、GUI、dedicated server、embedded payload、远端缓存、socket、配置目录和 macOS bundle 必须使用同一变体；普通 `cargo build` 是优化后的 dev，不能从 opt-level 推断 release。GPUI revision、Linux cross linker、macOS framework stubs 和预编译 Metal shader 的具体命令见 [docs/Documents.md](docs/Documents.md)。

## 验证入口

模型和协议：[`tests/phase1.rs`](tests/phase1.rs)、[`tests/phase2.rs`](tests/phase2.rs)、[`tests/protocol.rs`](tests/protocol.rs)。终端可靠性：[`tests/phase3.rs`](tests/phase3.rs)、[`tests/terminal_reader.rs`](tests/terminal_reader.rs)、[`tests/terminal_detach.rs`](tests/terminal_detach.rs)、[`tests/resize_stream.rs`](tests/resize_stream.rs)、[`tests/hotpath.rs`](tests/hotpath.rs)。真实 control/GUI 路径：[`tests/ctl_smoke.rs`](tests/ctl_smoke.rs)、[`tests/scenarios/`](tests/scenarios/)。变体打包：[`tests/build_variants.rs`](tests/build_variants.rs)。

验证必须使用有界 operation/output/exit 条件、独立 socket/config 和 Water control API；历史“通过”或“预存失败”不自动豁免当前基线。
