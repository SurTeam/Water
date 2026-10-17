# 服务端与远程连接

实现入口：`internal/goserver/`、`internal/gomodel/`、`internal/goprotocol/`、`internal/goclient/`、`internal/goremote/`。整体数据流见 [整体设计](README.md)，恢复事务见 [兼容与恢复](server-recovery.md)，浏览器权限见 [Server Web](browser-access.md)。

## 必须保持的边界

- 外部模型修改必须走 command → dispatcher → model。UI、`water ctl`、scenario 和远端控制通过 transport/channel 调用，不能直接改模型或引入全局共享模型。
- 服务端拥有模型、PTY 元数据和按保留策略管理的原始 replay；客户端拥有模拟、选择、滚动与预览。禁止恢复逐屏服务端快照。终端流契约见 [终端管线](terminal-rendering.md)。
- 实体、connection、operation 使用 typed ID 与完整 UUIDv4；JSON/CLI 使用 UUID 字符串，二进制保留全部 16 字节。禁止集合下标、计数器、截断和浮点身份。命令路由到目标对象所属 connection。
- 协议版本、API signature、能力与构建 revision 以 `internal/goprotocol`、`internal/gobuild` 为准。产品版本不是兼容性判断条件。不兼容时保留诊断入口并阻止普通操作。
- 每个窗口独立保留 workspace/tab/pane 选择。共享快照不得覆盖其他窗口选择；仅焦点 session 自动修改共享 PTY 网格。获得焦点时重新提交当前几何，客户端像素度量不能因未聚焦而停止更新。
- listener 整个生命周期持有锁，不能替换活跃 socket；删除时校验自己创建的 inode。退出和共享 server 生命周期使用已有 session，Web listener 的存活也纳入判断。
- SSH 使用系统 OpenSSH ControlMaster 与 Unix socket forwarding。远端 endpoint 稳定发现，payload 按变体、平台、版本、协议与 server revision 隔离；歧义或不可探测的旧实例明确报错，不能通过启动空 server 掩盖。
- GUI 远端重连保留 connection/pane 身份与设置草稿，健康检查和显式 server 操作按 connection 串行；RPC、SSH 与关闭等待均须有界。

## 当前命令与连接逻辑

客户端按对象 connection 建立 session，session.open 双向判断协议、API signature 和能力，不兼容则只开放诊断。普通变更通过 dispatcher 更新模型，再用 operation/revision 与客户端投影同步；GUI 的本地导航、选择和 preview 不代替共享模型变更。

启动探测发现正在监听的 socket 时，不因暂时超时就 unlink 或替换；listener 持锁并按 inode 清理自己的路径。SSH 先发现稳定 endpoint，复用可诊断旧实例，避免产品升级新建一套空工作区；缓存 payload 与 endpoint 身份分开管理。

GUI 多窗口按 focus 决定自动 PTY resize。TUI 也声明焦点；浏览器使用受限 session，不具备恢复、管理或 GUI 自动化权限。Go 桌面/TUI 正常 socket 路径的终端流独立于控制 RPC；浏览器复用 WebSocket。具体背压与已知缺口见 [性能与资源管理](performance.md)。原始 replay 的行预算与单帧 attach 边界见 [终端输出与渲染](terminal-rendering.md)。

## 验证

协议/dispatcher/model/session 改动先运行相关包的单元与 race 检查；远端、焦点和生命周期变化另用真实多窗口/SSH fixture。恢复不能只断言进程重启，必须验证拓扑、目录、选择、失败后 artifact 保留及用户实例未被操作。可复用入口见 [测试策略](testing.md)。
