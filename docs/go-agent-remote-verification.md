# Go Agent、侧边栏、远程与通知验证

## 变更

- 侧栏底部“连接远程”“新建工作区”按钮分别提供高度与字号设置：`sidebar_remote_button_height/font_size`、`sidebar_workspace_button_height/font_size`；高度范围 8–96，字号范围 6–32。列表为实际按钮高度预留空间，按钮间距 8，保存后立即生效。
- Workspace 下的 Agent 最左侧使用普通字体绘制 Unicode `▶`（运行）/`Ⅱ`（停止或离线），替换原来的小圆点，右侧不再占用状态区域；集中模式继续显示 Workspace 名称和状态文字。集中模式填满下半区行宽，不应用 Agent/Workspace 行宽比例；Workspace 名称跟随重命名。PTY 退出后仍遵循现有自动关闭 pane 的行为，对应 Agent 行随 pane 移除，不残留运行中状态。
- 新建 tab/pane 在模型切换选择前捕获源 pane 目录。服务端优先查询源 PTY 前台进程的实时目录，其次回退到客户端通过 AppCommand 传递的 OSC 7 file URI，再回退到模型/配置目录。macOS 查询在服务端 worker 上通过限时 1 秒的 lsof 完成，Linux 使用 `/proc/<pid>/cwd`；无 GUI 主线程阻塞。已验证中文/空格目录、新 tab、新 split，以及旧 OSC 7 后再次 cd 的实时目录继承。
- Cmd+C 只复制；无选区时不向 PTY 写入字节。Ctrl+C 保持真实中断，Command/Super 不编码成 Control；同样禁止 Cmd+D 隐式转换 EOF，默认 EOF 改为 Ctrl+D。保留旧 `copy_or_interrupt` JSON key 的读取兼容，设置界面名称改为“复制”。真实 raw PTY 验证 Cmd+C/Cmd+D 无多余字节、Ctrl+C 正确发送 `0x03` 并中断进程。
- Water 普通终端小幅滚动移动一行，快速聚合滚动保留幅度：40 逻辑像素为一步，120 为三步，4000 为 100 行；Gio 路径按 DPI 换算。单次本地历史滚动上限 65536 行并由 viewport 限制在历史范围内，应用鼠标模式单次最多发送 64 份标准事件，避免大量输入阻塞 UI。真实 raw PTY 验证 dy=40/120/10000 分别收到 1/3/64 份 SGR wheel report；真实 GUI 验证一行、100 行、到达历史顶部和返回底部。Codex 0.160.0 自身仍固定每份 wheel report 移动三行：[官方源码](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/transcript_view/input.rs#L222-L223)。因此没有把 Codex 应用内部滚动一行标记为已实现；这需要 Codex 提供可配置步进或修改其实现，标准鼠标协议无法指定移动行数。
- Unicode 文本保留字体原点、左右留白与共同基线，不再因为双宽格按墨迹边界居中。覆盖中文标点与括号、全角字符、日韩文字、组合字符、数学符号、箭头、扩展几何/棋牌/圈字符号；框线、块元素、盲文继续按 cell 几何绘制。仅 emoji 和私用图标进行格内适配；emoji 判断使用 [Unicode 17 Emoji_Presentation 数据](https://www.unicode.org/Public/17.0.0/ucd/emoji/emoji-data.txt)，尊重 VS15 文本显示和 VS16 emoji 显示，不再把整个 U+1F000–U+1FAFF 区域视作 emoji。位置单元测试及真实 GUI 截图通过；测试字体不包含的扩展字符仍显示缺字框，完整 ZWJ emoji 合成不属于本次位置修复。
- Agent 自动名称读取标准 OSC 0/2 标题，支持 BEL/ST 结束、分片、Unicode、后台 pane 与 replay；清空标题或未发出标题时回退 Agent 类型名，显式手动重命名优先。标题由客户端 emulator 投影，不解析 Agent 私有会话文件、不修改服务端模型。四个 CLI 是否提供其内部会话名称取决于它们是否发出 OSC 标题；不把通用进程存活状态猜成“思考中/等待输入”。

- 前台 PTY 进程刷新会同步 Agent 绑定；覆盖 Claude、Pi、Codex、OpenCode，包括 Node 包路径。回到 shell 时移除运行绑定，同一种 Agent 的自定义名字保留。
- `ui.sidebar_agent_mode`：`workspace` 在 Workspace 下展示；`split` 在侧栏下半区集中展示。上下区独立滚动，Agent 点击继续通过所属 connection 的 tab.activate / pane.focus 命令定位。
- 主机、Workspace、Agent 分别提供字号、文字对齐和行宽比例。主机与 Workspace 新增行宽设置，已有独立行高和 Agent 行宽设置继续生效。
- `sidebar_host_font_size`、`sidebar_workspace_font_size`、`sidebar_agent_font_size` 范围 6–32；对应 `*_alignment` 支持 `left`、`center`、`right`；对应 `*_row_width` 为 0.2–1 的比例。
- 标题栏右侧的两个分屏按钮和设置按钮已移除；继续使用原有快捷键和菜单。`titlebar_height` 最小 12，`tab_height` 最小 8，分别配置；标签页高度不超过标题栏，并垂直居中。移除字号对标题栏高度的隐式扩张；标签页左边缘对齐 terminal 容器。UI/标签/侧栏字号最小 6，侧栏最小宽度 64，主机/Workspace/Agent 行高最小 8。
- 设置面板忽略背景点击；保存成功保持打开，重建归一化后的字段与修改基线，支持连续保存。取消、Escape 或设置快捷键关闭面板；有修改时首次退出展示提示，再次明确退出才丢弃。重连保留设置草稿。二级标题使用强调色，分类、选择和底部按钮增加间距。
- 远程健康检查每 2 秒一次，单次 RPC 限制 1 秒，连续 3 次失败进入重连；已有 session 失败直接进入重连。失败重试从 2 秒逐步退避到 30 秒。成功恢复保持 connection UUID、pane UUID 和当前选择。
- Session RPC 默认限时 10 秒，无响应关闭 session 并释放阻塞 writer。SSH 探测、传输和控制命令限时 12 秒；ControlMaster 按运行变体、目的地和 SSH 配置隔离，长临时目录回退到 `/tmp`。
- `ui.system_notifications` 默认关闭，可在设置启用。macOS 使用 UserNotifications 的系统授权与投递接口；通知涵盖 Agent 停止、连接丢失/恢复，以及终端 BEL 铃声。铃声按 terminal UUID 在 5 秒内合并，attach replay 不触发历史铃声通知。
- 系统通知需要有 Bundle ID 的 macOS app；普通 CLI 二进制跳过系统投递。`ui snapshot` 的 `native_menu.notification_status` 报告 bundle 缺失、授权拒绝、投递失败或已排程状态。

## 验证命令

```sh
go test ./internal/... -timeout 55s
go vet ./internal/goagent ./internal/goclient ./internal/gomodel ./internal/goterminal ./internal/goconfig ./internal/goui ./internal/goremote ./internal/gouiapp
go test -race ./internal/goclient ./internal/goui ./internal/goremote ./internal/gomodel ./internal/goterminal ./internal/govt -timeout 55s
go build -o target/go-ui-smoke/water ./cmd/water
go build -o target/go-ui-smoke/water-server ./cmd/water-server
~/.venv/bin/python scripts/local-ssh-smoke.py
~/.venv/bin/python scripts/local-ssh-smoke.py --gui
~/.venv/bin/python scripts/go-ui-agent-smoke.py
~/.venv/bin/python scripts/go-ui-settings-smoke.py
~/.venv/bin/python scripts/go-ui-background-smoke.py --background-mode hide --background-steps 2,5,10 --workload ansi --rate-mib 1
~/.venv/bin/python scripts/go-ui-notification-smoke.py
```

本地 SSH 脚本创建唯一目录、密钥、配置和 loopback 监听端口；记录并清理自己的 sshd、server 和 ControlMaster。通过真实 SSH 建立 Water session，然后只模拟 disconnected 状态验证重连，检查 connection/pane 身份和未保存的设置草稿。另有内存 transport 模拟延迟及无响应；不修改系统网络配置，不断开任何真实网络连接。

## 本次结果与限制

单元测试、侧边栏目标几何测试、通知去重、BEL 解析、四种 Agent 的真实 PTY 命名探针，以及真实本地 SSH/模拟失败重连已通过。已安装 CLI 的版本命令返回 Claude 2.1.285、Pi 1.0.0、Codex 0.160.0、OpenCode 1.18.34。四个真实 CLI 已在隔离 GUI 中启动并被正确识别，没有向模型提交任务；四个并存的 PTY Agent 探针逐一通过实际侧栏点击切换到精确 pane UUID。

先前 macOS session 的 `CGSSessionScreenIsLocked=1`、`active_displays=0`，已有 Water 的 ping/ui snapshot 可响应，新 Ebitengine GUI 启动报 `ui: no monitor was found at initializeGLFW`。2026-10-04 后续验证时显示器恢复（active_displays=1、locked=false），已完成新版本真实 GUI 验证。仍未把新 GUI 在无活动显示器时启动标记为通过，也没有自动锁定或解锁用户桌面。

GUI 脚本通过 Water 自身 `ui click`、`ui key` 和 model command 路径检查两种布局与取消确认，使用独立 socket/config/PID。Agent 多 pane 点击、背景点击保持设置打开、脏草稿取消确认、保存后保持打开与连续保存、保存后的取消/Escape/快捷键退出、完整设置回归和截图检查均通过。20/12 与 12/8 的标题栏/标签页高度通过真实 GUI 几何断言和截图检查；右侧按钮不再生成 hit，标签页与 terminal 容器左边缘一致，窗口按钮点击区完全位于小高度标题栏内。中英文切换、重启持久化、字体与终端实际 stty 网格、原生窗口圆角/拖动/缩放/分屏回归通过。GUI 本地 SSH 验证通过远程终端 marker 往返，并从本地 connection 点击远程 Agent，确认恢复到正确 connection/pane。持续 ANSI 输出下的隐藏/恢复窗口 2、5、10 秒测试通过；隐藏窗口不等同于真实锁屏，启动后的实际锁屏持续交互仍未验证。

真实 CLI 启动测试发现替换终端时 GUI 向已关闭 emulator 更新窗口尺寸的空指针崩溃。已在 terminal mutex 下检查 emulator 存活，并对已关闭终端的后续事件进行保护；新增回归测试与竞态检查通过，修复后真实 CLI 测试通过。

macOS 通知使用独立、临时 ad-hoc 测试 bundle 验证真实系统回调，结果为 `permission_denied`。没有将“系统拒绝但不崩溃”标记为通知实际显示成功。用户明确选择不更改通知权限、不等待投递重测；实际授权后的横幅显示保留为未验证项。

后续测试产物：

- Agent 启动、OSC 标题、左侧 Unicode 状态替换圆点、两个 Workspace 集中展示、改名与退出生命周期回归：`/tmp/water-agents.7gx8lvaq/`
- 完整设置 GUI 回归、实时目录继承、Command/Control raw PTY 字节、细粒度与大量滚动、Unicode 文本位置、底部按钮独立高度/字号、最小标题栏与强调色截图：`/tmp/water-settings.l5ws1wsr/`
- 原生圆角、布局与快捷键回归：`/tmp/water-parity.l6cy70xr/`
- 中英文、重启持久化及真实 shell 网格：`/tmp/water-language-grid.y_wnrlj7/`
- 真实 SSH GUI 与跨 connection 点击：`/tmp/water-ssh-test.wsco1yk5/`
- 1 MiB/s ANSI 输出、隐藏/恢复：`/tmp/water-background.7wbgvjv9/`
- 系统通知拒绝回调：`/tmp/water-notify.bz2vgjj0/`

本次未操作用户现有 pane，也未终止用户 GUI/server；各测试记录并清理自己的 GUI/server/sshd/ControlMaster。

系统通知接口依照 [Apple UserNotifications 授权接口](https://developer.apple.com/documentation/usernotifications/unusernotificationcenter/requestauthorization(options:completionhandler:)) 和 [通知中心接口](https://developer.apple.com/documentation/usernotifications/unusernotificationcenter) 实现。
