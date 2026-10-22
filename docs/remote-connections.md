# 服务端与远程连接

实现入口：`internal/goserver/`、`internal/gomodel/`、`internal/goprotocol/`、`internal/goclient/`、`internal/goremote/`。整体数据流见 [整体设计](README.md)，恢复事务见 [兼容与恢复](server-recovery.md)，浏览器权限见 [Server Web](browser-access.md)。

## 必须保持的边界

- 外部模型修改必须走 command → dispatcher → model。UI、`water ctl`、scenario 和远端控制通过 transport/channel 调用，不能直接改模型或引入全局共享模型。
- 服务端拥有模型、PTY 元数据和按保留策略管理的原始 replay；客户端拥有模拟、选择、滚动与预览。禁止恢复逐屏服务端快照。终端流契约见 [终端管线](terminal-rendering.md)。
- 实体、connection、operation 使用 typed ID 与完整 UUIDv4；JSON/CLI 使用 UUID 字符串，二进制保留全部 16 字节。禁止集合下标、计数器、截断和浮点身份。命令路由到目标对象所属 connection。
- 协议版本、API signature、能力与构建 revision 以 `internal/goprotocol`、`internal/gobuild` 为准。产品版本不是兼容性判断条件。不兼容时保留诊断入口并阻止普通操作。
- 每个窗口独立保留 workspace/tab/pane 选择。共享快照不得覆盖其他窗口选择；仅焦点 session 自动修改共享 PTY 网格。获得焦点时重新提交当前几何，客户端像素度量不能因未聚焦而停止更新。
- listener 整个生命周期持有锁，不能替换活跃 socket；删除时校验自己创建的 inode。退出和共享 server 生命周期使用已有 session，Web listener 的存活也纳入判断。
- SSH 使用系统 OpenSSH ControlMaster 与 Unix socket forwarding。远端 endpoint 稳定发现，payload 按变体、平台、版本、协议与 server revision 隔离；可诊断的旧实例优先复用并提示升级，完全不可兼容时在旁边启动本版本实例并明确告知，旧实例原样保留，不得删除、替换或用空 server 冒充。
- GUI 远端重连保留 connection/pane 身份与设置草稿，健康检查和显式 server 操作按 connection 串行；RPC、SSH 与关闭等待均须有界。

## 当前命令与连接逻辑

客户端按对象 connection 建立 session，session.open 双向判断协议、API signature 和能力，不兼容则只开放诊断。普通变更通过 dispatcher 更新模型，再用 operation/revision 与客户端投影同步；`pane.execute` 是直接返回结果的有界终端 RPC，解析目标时短暂持有命令锁，执行等待不持有全局锁，且不创建 operation；GUI 的本地导航、选择和 preview 不代替共享模型变更。

启动探测发现正在监听的 socket 时，不因暂时超时就 unlink 或替换；listener 持锁并按 inode 清理自己的路径。SSH 先发现稳定 endpoint，复用可诊断旧实例，避免产品升级新建一套空工作区；缓存 payload 与 endpoint 身份分开管理。

远端存在两套 endpoint：稳定 endpoint 用于跨版本复用，side endpoint 在路径中带协议与 API signature 身份，仅在稳定 endpoint 被不可兼容实例占用时使用。占用判定用 `test -S` 区分"尚无 server"（照常在稳定 endpoint 启动）与"有但无法通信"（转 side endpoint）；再次连接复用已存在的 side 实例，不重复启动。

GUI 多窗口按 focus 决定自动 PTY resize。TUI 也声明焦点；浏览器使用受限 session，不具备恢复、管理或 GUI 自动化权限。Go 桌面/TUI 正常 socket 路径的终端流独立于控制 RPC；浏览器复用 WebSocket。具体背压与已知缺口见 [性能与资源管理](performance.md)。原始 replay 的行预算与单帧 attach 边界见 [终端输出与渲染](terminal-rendering.md)。

## SSH 认证与诊断

GUI 手动连接先复用 ControlMaster，再尝试非交互认证。仅在 OpenSSH 返回认证拒绝时通过私有 askpass helper 请求密码或 keyboard-interactive 输入；网络、主机密钥、配置和部署错误直接展示诊断，不误报为密码问题。密码输入遮蔽，凭据通过当前尝试的私有 Unix socket 传递，不放入命令参数或环境变量。认证支持取消并有超时。后台自动重连以及 GUI 启动前处理的 `--ssh` 保持非交互，不反复弹出认证框。

连接尝试独立收集 SSH stderr，机器可读探测仅解析 stdout；成功命令的 warning 不得丢弃或污染平台、socket 和 descriptor 解析。但 OpenSSH 控制命令的结果也写 stderr：`-O check` 成功时打印 `Master running (pid=N)` 并返回 0，这类状态行描述的是控制操作本身，不是连接诊断，不得作为 warning 展示；其余成功命令的 stderr 仍按 warning 保留。连接失败保留此前收集的诊断，成功后在新 connection 的远程面板显示 warning。完整错误的换行与滚动交互见 [界面布局与交互](ui-design.md)。

远端命令由远端登录 shell 解释执行，探测命令不能依赖其 glob 语义：zsh 在未匹配时直接以 `no matches found` 失败，而 sh/bash 会把 pattern 原样传入。旧实例探测固定用显式 `sh -c` 包裹，避免 shell 差异变成连接失败。

## 远端 server 版本不一致

连接成功后按 `goprotocol.Assess` 判断远端 server 与本构建的关系，两种情况必须让用户可见：

- 兼容但更旧（revision 不同或 legacy 无 revision）：GUI 弹窗提示用当前版本重启，显示双方版本，用户可稍后。确认后走既有 recovery 重启：导出布局 → 关闭旧 server → 启动本版本 payload → 恢复布局。无 `recovery/v1` 的旧 server 无法安全导出布局，只提示在远端人工停止后重连，不自动重启。
- 完全不可兼容（协议、API signature 或变体不符，或 endpoint 被无法探测的实例占用）：旧 server 保持运行，本版本在 side endpoint 启动新实例（空工作区），并弹窗告知旧实例仍在其 socket 上运行。不导出旧实例布局，也不尝试关闭它。

远端部署需要缓存所在文件系统有可写空间；满盘会阻止 payload 部署。此类环境错误必须明确呈现，不能靠密码重试掩盖，也不能自动删除远端用户文件。

## 验证

SSH 认证和诊断变化须覆盖密钥拒绝后的 GUI 密码输入、取消、非认证错误不触发密码回退、完整长错误滚动，以及成功连接时的 warning；使用独立 config/socket 的 GUI 和拥有明确进程归属的 SSH fixture，保留现有用户实例。机器输出与 stderr 分离另做解析回归。

远端版本差异须在真实主机上验证：兼容旧版本弹窗并可重启（布局保留）、不可兼容时新实例与旧实例并存且旧实例仍在应答、稍后路径不触发重启。以 `WATER_TEST_INSTANCE`、独立 config/socket 的隔离实例执行，结束后只清理自己创建的进程、socket 与 ControlMaster。

协议/dispatcher/model/session 改动先运行相关包的单元与 race 检查；远端、焦点和生命周期变化另用真实多窗口/SSH fixture。恢复不能只断言进程重启，必须验证拓扑、目录、选择、失败后 artifact 保留及用户实例未被操作。可复用入口见 [测试策略](testing.md)。
