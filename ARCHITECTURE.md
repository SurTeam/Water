# Water 架构

当前产品由 Go 实现，桌面运行时使用 Ebitengine。工作约定见 [AGENTS.md](AGENTS.md)，命令和发布流程见 [docs/Documents.md](docs/Documents.md)。

## 入口地图

| 关注点 | 入口 |
| --- | --- |
| GUI/CLI 启动、control 和 scenario | [`cmd/water/`](cmd/water/)、[`internal/gouiapp/`](internal/gouiapp/) |
| 独立 server | [`cmd/water-server/`](cmd/water-server/)、[`internal/goserver/`](internal/goserver/) |
| 应用模型、workspace/tab/pane、命令和事件 | [`internal/gomodel/`](internal/gomodel/)、[`internal/goserver/commands.go`](internal/goserver/commands.go) |
| 协议、UUID、operation 和 revision | [`internal/goprotocol/`](internal/goprotocol/) |
| PTY、前台进程、回放和有序终端流 | [`internal/goterminal/`](internal/goterminal/) |
| RPC、GUI session 和有界传输队列 | [`internal/goclient/`](internal/goclient/) |
| 客户端模拟、终端查询和图形解析 | [`internal/govt/`](internal/govt/)、[`internal/xterm/`](internal/xterm/) |
| 窗口、布局、终端绘制、输入和 Settings | [`internal/goui/`](internal/goui/) 中的 `ebiten_*` 和共享客户端模块 |
| 服务端 Web、HTTP/HTTPS/WebSocket、配对与浏览器客户端 | [`internal/goserver/web.go`](internal/goserver/web.go)、[`internal/goserver/web_auth.go`](internal/goserver/web_auth.go)、[`internal/goserver/webassets/`](internal/goserver/webassets/)，使用方法见 [docs/server-web.md](docs/server-web.md) |
| SSH 与 embedded server payload | [`internal/goremote/`](internal/goremote/) |
| Agent 检测与绑定 | [`internal/goagent/`](internal/goagent/) |
| 配置与构建身份 | [`internal/goconfig/`](internal/goconfig/)、[`internal/gobuild/`](internal/gobuild/)、[`VERSION`](VERSION) |

## 运行时分层

```text
GUI / water ctl / scenario / remote client
        │ 对象所属 connection 的 session / control transport
        ▼
server command dispatcher → application model
        ├─ operation / revision / bounded event history
        └─ PTY metadata / title / agent / exit

PTY reader → ordered Output / Resize / Exit
           → bounded replay + live stream
           → client emulator → visible snapshot → Ebitengine Draw
```

外部模型变更必须通过服务端命令路径。客户端拥有窗口选择、viewport、scrollback、selection 和拖拽预览；提交拓扑变化时才发送命令。服务端拥有模型、PTY 和有界原始 replay，不维持长期屏幕模拟器，不逐屏推送终端快照。

GUI 命令写入在 session worker 上执行，帧循环不做 socket I/O。session 写队列为 64 frame / 16 MiB，terminal decoded 队列为 64 event。队列饱和时明确断开 session，关闭会唤醒等待者；输出不能静默丢弃。PTY 的 Output、Resize、Exit 保持同一有序 sequence，attach replay 与 live 流按 sequence 去重、完整衔接。

## UI 与输入

[`internal/gouiapp/app.go`](internal/gouiapp/app.go) 启动桌面窗口。`ebiten_window.go`、`ebiten_layout.go`、`ebiten_terminal.go` 和 `ebiten_input.go` 实现无系统装饰窗口、标题栏、控件、终端绘制和输入。原生输入与 `water ctl ui` 使用同一套事件处理器；控制请求经过有界队列进入 Update，截图捕获真实 Draw 帧。PNG 编码、配置保存、SSH 和字体文件加载在帧循环外进行。

共享 `WorkspaceClient` 持有 connection、客户端终端、Settings 和 selection；旧 Gio 布局与输入模块保留用于 Go 内部兼容性测试，运行时不创建 Gio 窗口。Ebitengine 的单进程窗口限制通过同变体 GUI 子进程连接已有 server 处理。UI control 总是路由到目标窗口的活动 connection。

终端客户端处理宽字符、字体 fallback、selection/copy、OSC 8 hyperlink、图形、终端查询回复与本地/远端文件打开。重放不触发剪贴板或终端报告等副作用。macOS 原生菜单与圆角 layer 操作经过有界 main-queue bridge。

## Connection、远端与身份

每个 Local/Remote connection 有独立 transport、model projection 和 client terminal state。实体与 operation 使用完整 UUIDv4；JSON/CLI 传递 UUID 字符串，二进制终端事件保留完整 16 字节。命令必须发到对象所属 connection。

控制协议版本为 5，API signature 为 `water-control/v6`。JSON control frame 使用 32-bit big-endian 长度前缀，live terminal frame 仍使用 `\0WT4` 和 UUID/sequence/geometry 布局。`server.inspect` 提供跨应用协议的只读探测，`session.open` 双向校验能力；不兼容连接保留诊断界面并拦截普通操作。产品版本不作为兼容性条件，打包时另生成 server dependency source revision。Settings → Server 提供检测、布局保存与有保护的重启恢复；恢复通过 dispatcher 提交，先持久保存当前目录和布局，再关闭旧 server。详细契约与旧版限制见 [docs/server-compatibility.md](docs/server-compatibility.md)。

Cmd+N 启动独立 GUI 进程并复用当前 socket/config；各客户端保留自己的 workspace、tab、pane 选择，通过 `push.selection` 接收仅属于自身的命令选择结果。共享模型快照不会覆盖其他窗口的选择。GUI session 使用完整 UUID window ID；`session.focus` 声明焦点，server 只接受当前焦点 session 的自动 PTY resize，客户端获得焦点时重发当前几何。socket listener 持有整个生命周期的文件锁，拒绝替换活跃 listener，清理只删除自己创建的 inode。退出使用已有 session，`detach_on_quit=false` 时最后一个 GUI release 才关闭 server；embedded server 的宿主在窗口关闭后可继续服务其他 GUI，直到所有 GUI release。

SSH 使用系统 OpenSSH ControlMaster 和 Unix socket forwarding。首次连接按远端 Darwin/Linux、amd64/arm64 选择 embedded Go server payload；远端 payload 缓存按产品版本、server revision、协议、namespace 和平台隔离，socket 按变体与 destination 稳定发现，并探测旧版版本化 endpoint。产品升级不自动新建空工作区；旧实例歧义或无法探测时明确报错，不静默替换。断线保留 offline host 与服务端 workspace。

Agent 检测来自终端前台进程及 argv，绑定、状态和侧栏交互沿既有 model/connection 路径进行。

## Server Web 直连

server 可选启用独立 HTTP/HTTPS 与 WebSocket listener，提供自托管浏览器终端和一次性配对入口。GUI Settings → Server 通过当前 connection 的既有 IPC/SSH session 配置、启动/停止目标 server 的 Web 服务、显示二维码和撤销设备。浏览器直接访问 server 派生的访问地址；HTTP 监听地址和端口分开配置，默认 `127.0.0.1:8080`，可手动设置 `0.0.0.0` 监听所有 IPv4 网卡，二维码使用实际 IP。HTTP 无需证书并使用 WS，HTTPS 校验证书并使用 WSS；已配对设备的摘要保存在配置文件旁，身份不依赖控制 socket，可跨 server 重启恢复。网络可达性由 Tailscale 等外部环境保证，无中继或 NAT 穿透。

WS adapter 使用受限的已认证 browser session，共用命令 dispatcher、模型和有界终端流。浏览器拥有独立选择、终端模拟及 viewport，不能调用 Web 管理、recovery、GUI automation 或 server shutdown。Web listener 与浏览器会话纳入 server 生命周期，避免最后一个 GUI release 误关可直连的 server。详见 [docs/server-web.md](docs/server-web.md)。

## 配置与构建

配置类型、默认值、override 合并、校验与保存位于 `internal/goconfig`；完整示例为 [`config.example.json`](config.example.json)。Settings 保存保留未知字段，配置必须贯通读写和运行时投影。启动、shell、server 与历史预算变更的生效范围不能与当前 PTY 生命周期混淆。

Go 版本和依赖由 `go.mod`/`go.sum` 管理。产品版本由 `VERSION` 管理，打包和发布使用同一 `WATER_APP_VERSION` override。普通 Go 构建默认 dev；打包通过 `WATER_APP_VARIANT` 和 linker flags 设置 dev/release。GUI、server、embedded payload、socket、配置目录与 Bundle ID 必须保持同一身份。GUI 原生构建，headless server payload 通过 Go 交叉编译。

## 验证与发布

各模块的 `*_test.go` 覆盖协议、模型、PTY、客户端、VT、配置和 UI。[`internal/gobench/`](internal/gobench/) 提供终端性能与交互延迟检查；[`tests/scenarios/`](tests/scenarios/) 和 `scripts/run-go-scenario-suite.sh` 验证真实服务端命令路径；`scripts/run-go-ui-smoke.sh` 与各 `go-ui-*-smoke.py` 验证原生 GUI。

测试使用唯一 socket、临时 config、有界 operation/output/exit 条件，只清理本次创建的 PID。headless 或交叉编译不能替代真实 GUI 检查。

CI 只保留 [macos-signed.yml](.github/workflows/macos-signed.yml)。本地构建 unsigned archive 并验证后，上传目标 tag 的 draft release，再手动触发 signing workflow。Action 只下载、签名、验证、可选 notarization 和发布，不编译源码。
