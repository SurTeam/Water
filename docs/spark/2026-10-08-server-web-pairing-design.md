# Water server Web 直连与 GUI 配对设计

日期：2026-10-08
状态：讨论结论已整理，待用户审阅；本文不代表实现已经完成。

## 目标与边界

water-server 提供独立 HTTP/HTTPS 与 WebSocket 端口和浏览器前端。按当前实现要求，允许先以 HTTP/WS 跑通配对和终端流程；HTTPS/WSS 作为可选模式保留，HTTP 不加载或校验证书。桌面 GUI 的 Settings → Server 可以对当前 connection 所属 server 启动、停止 Web 服务，创建一次性配对邀请并显示二维码。本地 server 和通过 SSH 连接的远端 server 使用相同的控制语义。

手机与目标 server 的网络可达性由 Tailscale 或用户网络保证。手机直接访问目标 server，不经过桌面 GUI；关闭 SSH 连接不应终止已配对浏览器的直连会话。前提是目标 server 持续运行。

本阶段不引入公网配对服务、WebRTC、STUN/TURN、中继、自动 NAT 穿透，也不替换已有本地 IPC 或 SSH 传输。一个浏览器会话连接一个 server，不聚合桌面 GUI 的全部 connections。不增加文件浏览器或结构化 Agent Surface。

## 当前实现依据

- `internal/goui/ebiten_server_settings.go` 已提供 Server 面板及 Refresh、Save layout、Restart server、Retry restore 操作。
- `internal/goui/server_settings.go` 在后台执行 Server 操作，避免 Ebitengine 帧循环阻塞。
- `internal/goui/connections.go` 将操作绑定到 connection ID；`internal/goui/server_operations.go` 和 `internal/gouiapp/server_operations.go` 管理目标 connection 的操作。
- `internal/goserver/server.go` 接收现有 session 和控制请求；模型、PTY、原始 replay 由 server 持有。
- 当前未发现服务端 WebSocket、配对或二维码实现。本设计需要新功能，并非现有能力的配置开关。

## 架构选择

采用 server 内可选 Web listener，共用现有 session/command 服务。相较桌面 GUI 网关，此方案不依赖桌面进程转发，且手机可以直接访问远端 server。相较独立公网服务，此方案与 Tailscale 保证可达性的前提一致。

```text
桌面 GUI ── 已有 IPC / SSH transport ── server 管理命令
                                          │
手机 Web ── HTTP/WS 或 HTTPS/WSS 独立端口 ── 已认证 session
                                          │
                                dispatcher → model / PTY
                                          │
                                events / replay / live stream
```

HTTP/WS adapter 不直接修改模型。它负责认证、消息边界与连接生命周期，再调用共享的 session/command 路径。现有终端帧语义、完整 UUID、sequence、Output → Resize → Exit、有界 replay 和背压契约保持不变。WebSocket 消息和内部帧的映射需要明确实现，不能假定 WebSocket 就是原始 TCP 字节流。

## 配置与监听生命周期

Web 默认关闭。HTTP 服务使用独立的监听地址 `listen_address` 和监听端口 `listen_port`，默认 `127.0.0.1` 与8080；可手动设置 `0.0.0.0` 监听全部 IPv4 网卡；地址不带协议或端口。可选 `tls` 开关启用 HTTPS，并显示证书和私钥路径。随 server 启动选项独立保存。配置贯通默认值、override、校验、Settings、持久保存和运行时投影；旧 `public_url` 自动迁移为这些字段。

运行时 Start/Stop 不隐式改动重启后的启用选项。Start 幂等；启动失败返回具体错误，不能仅凭配置已启用显示 Running。运行状态区分 stopped、starting、running、stopping，并单独记录最近一次错误。Stop 关闭 Web listener 和其浏览器会话，不停止 server、SSH session 或 PTY。

`0.0.0.0` 监听全部 IPv4 网卡；服务端从启用的本机接口派生实际访问地址，二维码不使用通配地址。Host/Origin 仅允许属于当前 listener 的本机地址，并要求请求 Origin 与 Host 同源，所有接口仍需设备授权。具体 IP 或主机名可限制监听接口；不能使用 GUI 的 SSH 转发端点或 SSH alias。不自动猜测 SSH alias 对应的 Web 地址。端口占用、非本机绑定或 HTTPS 证书不匹配时明确报错。

选择 HTTPS 时在 server 终止 TLS，证书必须能被手机浏览器验证。HTTP 不要求证书。Tailscale 证书的取得和更新由部署环境负责；第一阶段不自动安装或配置 Tailscale。dev/release 的配置、授权存储和 server 身份隔离。端口占用直接报错，不抢占监听实例。

## Server 管理协议

新增可协商能力 `web-access/v1`。建议命令名为：

- `server.web.inspect`：返回状态、实际监听地址、公布 URL、错误及能力，不返回凭据。
- `server.web.start` / `server.web.stop`：通过目标 server 的管理命令路径控制 listener。
- `server.web.pair.create` / `server.web.pair.cancel`：创建或取消一次性邀请。
- `server.web.devices.list` / `server.web.devices.revoke`：查看及撤销已授权设备。

上述管理操作仅对已有可信管理连接开放；浏览器 session 默认没有创建新邀请、修改监听配置、撤销其他设备或关闭 server 的权限。协议实现必须显式标识连接的认证主体和管理权限，不能仅依据客户端自报身份授权。

服务端生成的邀请绑定 server 持久身份、build variant 和邀请有效期。身份不采用进程 PID、集合下标或易变端口。

## GUI 交互

当前 connection 的 Server 面板只保留“Web 服务”入口；点击打开独立模态弹窗，集中显示状态、目标 server、HTTP 监听地址与端口、Start/Stop、配置、配对和设备管理。底层设置不可点击，关闭或 Escape 返回原 Server 设置并保留其他草稿。弹窗首页提供“随 server 启动 Web”开关，运行中保存只改下次启动策略，不重启当前 listener。监听地址与端口分别持久保存，重开窗口及重启 server 后保持不变；HTTP 推荐固定8080，随机空闲端口仅用于隔离测试。配对界面显示二维码、可读地址、过期时间和 Cancel；邀请成功消费或到期后明确失效。

远端操作通过已建立 SSH connection 的 session 发往远端，不在本地启动替代 listener。二维码由当前 GUI 渲染，内容由远端 server 返回；远端无须图形环境或二维码显示能力。

切换 connection 后不得把旧邀请展示为新 server 的邀请。关闭配对面板取消尚未使用的邀请；已配对设备不受影响。缺少 `web-access/v1` 的 server 显示不支持及升级提示，不能自动重启或升级远端。

所有控制 I/O、证书读取和二维码生成在帧循环之外；Draw 只使用准备好的状态和图像。已有 GUI/control API 共用真实 action 路径。

## 配对与认证

已授权管理连接请求邀请。server 产生至少 256 bit 的安全随机一次性 token；有效期 5 分钟，取消、使用或 server 重启即失效。内存中只保存 token 摘要，不写入日志、模型快照或通用事件广播。

二维码 URL 使用服务端派生的实际 HTTP/HTTPS 访问地址，邀请 token 放在 fragment。Web 前端读取后立即清除地址栏 fragment，并经同源 HTTP/HTTPS POST 交换登录会话。fragment 不随普通 HTTP 请求发送；前端不得引入第三方脚本或分析服务。

本阶段扫码持有一次性邀请即代表授权，不额外要求第二次桌面确认。兑换必须原子地检查有效性并消费，重放失败。二维码包含秘密，GUI/CLI 的邀请结果不得进入普通诊断日志。

兑换后建立可撤销的浏览器设备会话，使用 HttpOnly、SameSite=Strict cookie；HTTPS 额外使用 Secure 和 __Host- 前缀，HTTP 使用独立名称及非 Secure cookie；设备授权可跨 server 重启保存。授权摘要与稳定 UUID 保存在配置文件旁的私有目录（目录0700、文件0600），目录身份由绝对配置路径和变体确定，不依赖 PID 或控制 socket；旧当前 socket 的授权目录可自动迁移。网页打开已使用或重启前的二维码时，优先复用仍有效的设备 cookie，无授权时才兑换一次性邀请。授权存储限制为当前系统用户访问，并与 dev/release 隔离；会话凭据只保存摘要。撤销设备立即阻止新请求并断开其活动 WebSocket。

WebSocket 校验认证及严格的同源 Origin；配对和其他有副作用的 HTTP 请求执行 Origin/CSRF 防护。未认证连接不得读取模型、replay 或终端数据。请求体、消息大小、握手时间、并发连接、邀请创建和兑换频率均须有上限。

选择 HTTPS/WSS 时提供浏览器到 server 的传输加密；HTTP/WS 不提供传输加密。不额外自建应用层加密。电脑到远端仍使用已有 SSH 加密。无需在二维码中分发 TLS 私钥、SSH 私钥或长期会话密钥。

## 浏览器会话与恢复

Web 提供最小可用的工作区/tab/pane 浏览、终端显示与输入、手机软键盘辅助键。浏览器独立拥有窗口身份、选择和 viewport，不复制桌面截图，也不覆盖其他客户端选择。终端模拟在浏览器执行，server 不新增长期屏幕模拟器。

浏览器重连后重新认证、协商协议和 server 身份，再按已有 replay/live 契约恢复。队列满或历史不足必须给出明确恢复结果，不静默丢弃输出。浏览器失焦/后台和重新前台的连接变化不得影响 PTY 存活。焦点和 resize 延续现有 session 所有权规则，避免手机和桌面同时改变几何。

浏览器 session 纳入 server 的客户端生命周期管理；GUI 退出时不能仅因“最后一个 GUI release”关闭仍有已认证浏览器使用的 server。单纯关闭浏览器或 Stop Web 不终止工作区任务；已有 detach/quit 配置的最终语义须通过同一 server 生命周期路径明确实施。

远端独立退出、主机休眠或 server 自身关闭仍会中断访问；本功能不保证目标进程始终运行。

## 验证与验收

- 两个独立测试 server 验证本地/远端目标路由：GUI 操作 A 不得改变 B。远端地址来自目标 server，不能把 SSH 本地转发端口编码进二维码。
- Start/Stop 的成功、失败、重复调用和端口冲突有确定行为；停止 Web 保留已有 SSH 连接和 PTY。
- 通过 SSH 启动远端 Web 并创建邀请；浏览器兑换后关闭本次 SSH 连接，验证浏览器仍可直连并操作原有终端。
- 邀请过期、取消、重复兑换、并发兑换只成功一次；未授权、错误 Origin 和被撤销设备无法访问终端。
- server 重启后旧邀请失效，既有设备授权按照持久化策略继续有效；dev/release 不互认凭据。
- 手机与桌面同时查看终端，验证独立选择、resize 所有权、真实输入和终端输出。
- 验证 disconnect/replay、背压、大量输出下输入延迟；记录实测数据，不预设 WebSocket 性能结论。
- GUI 验证使用 Water control API、独立 socket/config、真实 action/model 路径；TLS、浏览器和手机软键盘行为单独进行真实环境验证。
- 每项测试有行为断言、超时、证据目录和实例所有权，清理只针对本次创建的进程。

## 交付拆分

该目标包含服务端安全入口、浏览器终端客户端、GUI 配对三个关联部分。实现时按服务端协议与认证 → 最小 Web 客户端 → 本地/SSH GUI 操作与二维码的依赖顺序推进；完整验收以远端二维码配对后浏览器直连为准。本文仅固化设计，不自动开始实现。
