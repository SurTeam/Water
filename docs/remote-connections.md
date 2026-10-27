# 服务端与远程连接

实现入口：`internal/goserver/`、`internal/gomodel/`、`internal/goprotocol/`、`internal/goclient/`、`internal/goremote/`。整体数据流见 [整体设计](README.md)，恢复事务见 [兼容与恢复](server-recovery.md)，浏览器权限见 [Server Web](browser-access.md)。

## 必须保持的边界

- 外部模型修改必须走 command → dispatcher → model。UI、`water ctl`、scenario 和远端控制通过 transport/channel 调用，不能直接改模型或引入全局共享模型。
- 服务端拥有模型、PTY 元数据和按保留策略管理的原始 replay；PTY 本身与其中的程序由 supervisor 持有，服务端只是它的客户端（见下文 PTY 所有权）。客户端拥有模拟、选择、滚动与预览。禁止恢复逐屏服务端快照。终端流契约见 [终端管线](terminal-rendering.md)。
- 实体、connection、operation 使用 typed ID 与完整 UUIDv4；JSON/CLI 使用 UUID 字符串，二进制保留全部 16 字节。禁止集合下标、计数器、截断和浮点身份。命令路由到目标对象所属 connection。
- 协议版本、API signature、能力与构建 revision 以 `internal/goprotocol`、`internal/gobuild` 为准。产品版本不是兼容性判断条件。不兼容时保留诊断入口并阻止普通操作。
- 每个窗口独立保留 workspace/tab/pane 选择。共享快照不得覆盖其他窗口选择；仅焦点 session 自动修改共享 PTY 网格。获得焦点时重新提交当前几何，客户端像素度量不能因未聚焦而停止更新。
- listener 整个生命周期持有锁，不能替换活跃 socket；删除时校验自己创建的 inode。退出和共享 server 生命周期使用已有 session，Web listener 的存活也纳入判断。
- SSH 使用系统 OpenSSH ControlMaster 与 Unix socket forwarding。远端 endpoint 稳定发现，payload 按变体、平台、版本、协议与 server revision 隔离；可诊断的旧实例优先复用并提示升级，完全不可兼容时在旁边启动本版本实例并明确告知，旧实例原样保留，不得删除、替换或用空 server 冒充。
- GUI 远端重连保留 connection/pane 身份与设置草稿，健康检查和显式 server 操作按 connection 串行；RPC、SSH 与关闭等待均须有界。

## 当前命令与连接逻辑

客户端按对象 connection 建立 session，session.open 双向判断协议、API signature 和能力，不兼容则只开放诊断。普通变更通过 dispatcher 更新模型，再用 operation/revision 与客户端投影同步；`pane.execute` 是直接返回结果的有界终端 RPC，解析目标时短暂持有命令锁，执行等待不持有全局锁，且不创建 operation；GUI 的本地导航、选择和 preview 不代替共享模型变更。

新 tab 和 split 继承来源 pane 的实时目录，优先查询前台进程，再查询 shell；查询失败时依次使用客户端报告目录和 pane 元数据，空窗格再按 workspace 与默认目录补齐。macOS 的 `lsof` 子进程固定使用 UTF-8 locale，避免 C locale 把中文路径转成显示转义；仅接受存在的绝对目录。外部查询预算为 250ms，并避免 `lsof` 的阻塞式文件系统探测；该预算不包含前置 ioctl、后续目录校验及 shell 启动，不能视为整个创建命令的延迟上限。

`tab.new`、`tab.new_in_workspace` 和 `pane.split` 创建模型对象后若 shell 启动失败，必须撤销新 tab/pane 并恢复原 workspace/tab/pane 选择，不能遗留空窗格。回滚只恢复选择，不导入整份旧模型，以保留启动等待期间积累的终端元数据。

启动探测发现正在监听的 socket 时，不因暂时超时就 unlink 或替换；listener 持锁并按 inode 清理自己的路径。SSH 先发现稳定 endpoint，复用可诊断旧实例，避免产品升级新建一套空工作区；缓存 payload 与 endpoint 身份分开管理。

远端存在两套 endpoint：稳定 endpoint 用于跨版本复用，side endpoint 在路径中带协议与 API signature 身份，仅在稳定 endpoint 被不可兼容实例占用时使用。占用判定用 `test -S` 区分"尚无 server"（照常在稳定 endpoint 启动）与"有但无法通信"（转 side endpoint）；再次连接复用已存在的 side 实例，不重复启动。

GUI 多窗口按 focus 决定自动 PTY resize。TUI 也声明焦点；浏览器使用受限 session，不具备恢复、管理或 GUI 自动化权限。Go 桌面/TUI 正常 socket 路径的终端流独立于控制 RPC；GUI 进一步按 terminal 实例拥有独立订阅与 worker，满队列只背压该流。GUI 订阅按连接句柄持有和取消，迟到旧 attach 的取消不会 detach 同 UUID 的新订阅；初次 attach/replay 不占用共享模型消费循环。浏览器复用 WebSocket。具体背压与已知缺口见 [性能与资源管理](performance.md)。原始 replay 的行预算与单帧 attach 边界见 [终端输出与渲染](terminal-rendering.md)。

## SSH 认证与诊断

GUI 手动连接先复用 ControlMaster，再尝试非交互认证。仅在 OpenSSH 返回认证拒绝时通过私有 askpass helper 请求密码或 keyboard-interactive 输入；网络、主机密钥、配置和部署错误直接展示诊断，不误报为密码问题。密码输入遮蔽，凭据通过当前尝试的私有 Unix socket 传递，不放入命令参数或环境变量。认证支持取消并有超时。后台自动重连以及 GUI 启动前处理的 `--ssh` 保持非交互，不反复弹出认证框。

连接尝试独立收集 SSH stderr，机器可读探测仅解析 stdout；成功命令的 warning 不得丢弃或污染平台、socket 和 descriptor 解析。但 OpenSSH 控制命令的结果也写 stderr：`-O check` 成功时打印 `Master running (pid=N)` 并返回 0，这类状态行描述的是控制操作本身，不是连接诊断，不得作为 warning 展示；其余成功命令的 stderr 仍按 warning 保留。连接失败保留此前收集的诊断，成功后在新 connection 的远程面板显示 warning。完整错误的换行与滚动交互见 [界面布局与交互](ui-design.md)。

远端命令由远端登录 shell 解释执行，探测命令不能依赖其 glob 语义：zsh 在未匹配时直接以 `no matches found` 失败，而 sh/bash 会把 pattern 原样传入。旧实例探测固定用显式 `sh -c` 包裹，避免 shell 差异变成连接失败。

## 远端 server 版本不一致

连接成功后按 `goprotocol.Assess` 判断远端 server 与本构建的关系，两种情况必须让用户可见：

- 兼容但更旧（revision 不同或 legacy 无 revision）：GUI 弹窗提示用当前版本重启，显示双方版本，用户可稍后。确认后走既有 recovery 重启：导出布局 → 关闭旧 server → 启动本版本 payload → 恢复布局。无 `recovery/v1` 的旧 server 无法安全导出布局，只提示在远端人工停止后重连，不自动重启。
- 完全不可兼容（协议、API signature 或变体不符，或 endpoint 被无法探测的实例占用）：旧 server 保持运行，本版本在 side endpoint 启动新实例（空工作区），并弹窗告知旧实例仍在其 socket 上运行。不导出旧实例布局，也不尝试关闭它。

远端部署需要缓存所在文件系统有可写空间；满盘会阻止 payload 部署。此类环境错误必须明确呈现，不能靠密码重试掩盖，也不能自动删除远端用户文件。

## PTY 所有权与升级不断行

PTY 与其中的程序由独立的 supervisor 进程持有（`internal/gosupervisor`），server 通过 unix socket 成为它的客户端。经过 recovery 校验并导出布局的升级重启会保留程序，新 server 按 pane id 重新 attach，并用 supervisor 保留的输出历史重建 replay。普通关闭 server 则终止其终端程序并关闭独占 supervisor；仅关闭 GUI 而保留 server 不触发清理。

- 本地打包将 server 二进制另装为 `water-pty`（release）或 `water-pty-dev`（dev），优先以该名称启动 supervisor；旧安装包及仅携带 server 的远端 payload 回退到原 server 名称。`--pty-supervisor` 角色以新 session 启动，生命周期不跟随 server。默认启用，`WATER_PTY_SUPERVISOR=off` 退回进程内模式。
- GUI 内嵌 server 与独立 server 都必须调用同一个启用入口；否则本地重启会重新 spawn pane，而远端升级却能接回。
- supervisor 的键是 pane id（跨 server 重启稳定），terminal 实例 id 每次 spawn 都新建。两者必须分开：客户端按实例 id 订阅，替换 pane 的 terminal 时才会重新 attach；若让实例 id 等于 pane id，GUI 会继续挂在已退役的流上，OSC 标题、Agent 状态都会停在旧值。
- 普通 spawn 用 `Replace` 关闭该键上的旧程序；只有 recovery 导入时传 `resume`，才接回上一个 server 留下的程序。旧代的事件按 generation 丢弃，被替换的 terminal 从 registry 移除后其 Exit 不再关闭 pane。
- supervisor 按终端保留有界原始输出（上限 2 MiB，超出后保留最近的尾部）；重新 attach 时从指定 offset 继续，截断时明确标记，server 用自己的 replay 逻辑重建，不使用逐屏快照。
- 输出在 supervisor 内按 1 ms 空闲 / 5 ms 最大窗口合并后再转发，避免把每次 read 变成一个事件。
- resize 由 supervisor 的读循环串行处理：先 flush 已观察输出，再应用几何并发布 resize 事件，保证 server 不会先看到新几何。
- 每个连接一个写协程串行化所有帧（控制回复与终端输出共用 socket，不串行会交叉破坏帧）；订阅者队列满时只丢弃该订阅，不阻塞 PTY 读循环，也不关闭承载控制流量的连接。
- 只有已确认的 `recovery.shutdown` 才 detach 并保留程序；普通 `Close` 关闭 supervisor 并等待连接结束。supervisor 收到关闭请求后终止程序并退出进程。默认按控制 socket 隔离 supervisor；升级时发现旧版共享 socket 则兼容接回，普通关闭只清理自己的 pane，共享 supervisor 无剩余终端时才关闭。异常强制终止不能保证执行清理。
- 显式替换 pane 的 terminal 走 supervisor 的 `Replace`：先注册新 terminal 并把回复写到线上，再关闭旧程序，server 因此能在旧 Exit 到达前完成安装。
- supervisor 的 PTY 必须是 nonblocking 并注册到 runtime poller：阻塞读会占住线程，使并发的 `TIOCGPGRP` 查询（前台进程组，Agent 分类依赖它）等待数秒。
- 客户端须在发送请求前登记响应等待项，登记顺序与帧写入顺序共用同一个可取消的门；带终端身份的响应还须校验目标，避免后台采样与手动诊断并发时丢失或错配回复。请求写入与等待均受截止时间约束。
- 当前 supervisor 协议没有请求关联 ID；已发送请求取消后保留 FIFO 占位，迟到响应只消费原请求的位置。不能直接删除等待项而让旧响应满足新请求。永不返回的响应占位保留至连接关闭；协议字段保持兼容已有 supervisor。

性能边界：多一跳 unix socket，但保留了服务端原有的批处理与背压；PTY 读取不得因慢客户端阻塞，控制回复不得因输出拥挤而超时。终端流预算见 [性能与资源管理](performance.md)。

## 验证

SSH 认证和诊断变化须覆盖密钥拒绝后的 GUI 密码输入、取消、非认证错误不触发密码回退、完整长错误滚动，以及成功连接时的 warning；使用独立 config/socket 的 GUI 和拥有明确进程归属的 SSH fixture，保留现有用户实例。机器输出与 stderr 分离另做解析回归。

远端版本差异须在真实主机上验证：兼容旧版本弹窗并可重启（布局保留）、不可兼容时新实例与旧实例并存且旧实例仍在应答、稍后路径不触发重启。以 `WATER_TEST_INSTANCE`、独立 config/socket 的隔离实例执行，结束后只清理自己创建的进程、socket 与 ControlMaster。

supervisor 改动须覆盖：程序跨 server 重启存活（PID 不变、输出计数继续增长）、从 offset 重放不丢字节、resize/exit 与输出有序、历史有界但保留尾部、并发 spawn 各自拿到自己的回复、慢消费者不阻塞控制流量也不杀死程序。即时回复不得丢失，并发前台查询须返回各自终端的结果；取消后迟到回复不得污染后续查询，阻塞写入必须可取消。真实升级另需在远端主机上验证。

目录继承与创建失败回滚须覆盖 C locale 下的中文及空格路径、父 shell `cd` 后实时目录与启动元数据不同、进程查询失败时的目录回退，以及启动失败后的原布局与跨 workspace 选择保持。

协议/dispatcher/model/session 改动先运行相关包的单元与 race 检查；远端、焦点和生命周期变化另用真实多窗口/SSH fixture。恢复不能只断言进程重启，必须验证拓扑、目录、选择、失败后 artifact 保留及用户实例未被操作。可复用入口见 [测试策略](testing.md)。
