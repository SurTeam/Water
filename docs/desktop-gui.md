# Go 桌面界面与配置

桌面运行时使用 Ebitengine。`internal/gouiapp/app.go` 负责启动，`internal/goui/ebiten_window.go`、`ebiten_layout.go`、`ebiten_input.go`、`ebiten_terminal.go` 负责窗口、布局、输入与绘制，`internal/goui/workspace.go` 维护 connection 和终端客户端。Gio 路径保留用于内部兼容测试，不创建当前桌面窗口。

## 设计原则

- 窗口负责本地交互和视图，模型变更通过对象所属 connection 的命令路径提交；原生输入与 `water ctl ui` 共用动作处理，截图来自实际 Draw。
- 帧循环以交互和绘制为主；socket、SSH、持久化和重型解析交给 worker。不可变视图承担线程间画面交接，不能把“后台解析”理解为所有路径都已无锁等待，具体限制见下文。
- 配置必须贯通类型、默认值、override、校验、可达控件、保存往返与运行时投影。保存保留未知字段，生效范围不能混用“立即”“新窗口”和“重启服务”。
- 原生对象遵守平台线程归属；macOS autorelease pool 的创建、使用和释放固定在同一 OS thread。Go race 不能替代原生生命周期验证。

## 窗口与会话逻辑

Cmd+N 启动同变体的独立 GUI 进程，复用 socket 和配置。各窗口独立保留 workspace/tab/pane 选择；共享快照不覆盖其他窗口选择。焦点 session 负责自动调整共享 PTY 网格，失焦窗口仍更新自身像素度量；获得焦点时重发当前几何。

普通关闭释放自己的 session，server 生命周期按共享客户端、detach 策略和正在运行的 Web 服务判断。显式 Quit GUI and Local Server 会停止共享本地服务；不能把此动作等同于只关闭当前窗口。详见 [服务端与远程连接](remote-connections.md)。

Ebitengine 的 Water 维护副本位于 `third_party/ebiten`，具体差异见其 [WATER_CHANGES](../third_party/ebiten/WATER_CHANGES.md)。事件处理完成后才采样可见性；不可见帧继续允许 offscreen 绘制与截图，但不排入最终屏幕命令。Metal 缓存 drawable 显式持有跨 readback autorelease pool 的引用，并在呈现完成调用后释放，避免截图后复用失效对象。原生生命周期通过真实 macOS 读回回归验证。

显示器模式或缩放变化不产生 monitor connect/disconnect 事件，而显示器快照的 `contentScale` 与 bounds 在建立时读取一次。窗口实际 content scale 与缓存 monitor 不一致时必须重新枚举，否则长运行进程会按旧缩放绘制：framebuffer 小于窗口 backing store，被系统放大后整屏发虚，且只有重启才恢复。该判定只在两者不一致时触发，窗口 scale 未知时不视为不一致。

macOS 最小化恢复调用返回不代表动画及事件处理完成；AppKit/Dock 可在后续事件读取中暂时阻塞主线程。原生对照也可复现，不能把恢复期查询延迟等同于 CPU 开销或保证固定恢复时长。

圆角由透明窗口、缓存 shader 和 macOS layer clipping 实现；最大化/全屏使用方角。UI 尺寸、标题栏和 tabs 按配置及字体度量计算，布局和弹窗原则见 [界面布局与交互](ui-design.md)。

## 输入与终端呈现

按键处理在每帧 `Update` 内、二次 layout 之前完成，键盘与 IME 提交后立即写 PTY，不等布局；IME 重定位按几何与缩放去重，几何未变时不再每帧发起同步主线程调用。回显延迟与发布节奏见 [性能与资源管理](performance.md)。

Cmd+C 只复制，无选区不发送字节；Ctrl+C 才是中断，Command/Super 不隐式编码成 Control。Cmd+D 不隐式发送 EOF。文本粘贴走终端 bracketed-paste 状态；macOS 图片粘贴交给前台 Agent 的图像读取路径，不自动上传本地剪贴板到远端。

Cmd+V 对 Excel 等同时提供文本和图片的富剪贴板优先读取纯文本，保留 Tab 与换行。图片数据同时带有绝对图片路径或 `file://` URL 时仍优先图片；纯图片同样通过 Ctrl+V 交给应用读取。文件 URL 全部指向受支持的图片扩展名且剪贴板提供图片数据时走图片分支，其他文件或混合文件引用继续转成 shell 引用的路径文本，避免把 PDF 等文件的 TIFF 图标误当成图片粘贴。图片路径识别要求单个绝对路径、无 Tab 或换行，并匹配 PNG、TIFF、JPEG、HEIC、HEIF、GIF、BMP 或 WebP 扩展名；它不解码文件内容，也不保证接收端支持系统剪贴板。

IME composition/caret 使用统一坐标变换，平台缩放只应用一次。控制 API 注入用于验证共享处理器，真实系统键盘、IME 候选窗口仍需人工验收。

字体按配置的逗号分隔 family 顺序逐字形 fallback；字体元数据和 collection index 由实际字体文件读取。普通 Unicode 保留字体原点与共同基线，emoji/私用图标走专用适配；字形与选择的列宽语义统一由 [终端输出与渲染](terminal-rendering.md) 维护。

系统通知中的 Agent 注意提示来自已识别 Agent 的 OSC 状态变化，普通 BEL 不触发通知。Agent 持续运行不再产生“仍在运行”通知；长时间 shell 命令仍在完成或切换前台命令时按原规则通知。

## Settings 与配置生效

Settings 是客户端草稿，保存成功更新基线并保持面板打开；重连保留草稿。Cancel、Escape 和设置快捷键退出脏草稿时需要明确丢弃；关闭整个窗口或 Quit GUI 不走同一草稿确认流程。异步结果必须核验当前 pane、connection 与面板生命周期。

| 配置范围 | 当前生效方式 |
| --- | --- |
| 语言、主题、字体、快捷键、cursor_style/cursor_blink 等 | 按字段实时投影；应用可用终端序列覆盖光标偏好 |
| startup.window_columns/window_rows 初始网格 | 用于新窗口计算像素与默认网格，不重设已有窗口 |
| startup.window_min_columns/window_min_rows 最小网格 | Settings 标注 New windows；当前已有窗口在后续边框拖动时也读取新最小值，保存瞬间不自动调整窗口 |
| shell/server 与已有 PTY 生命周期相关选项 | 不改变已经启动的进程；按相应字段使用新终端或重启 server |
| GUI Settings 的 Agent/Web/Server 操作 | 使用所选 connection 的服务配置，不能替代成本地外观保存 |

完整字段见 [config.example.json](../config.example.json)。release 默认 `~/.config/water/config.json`，dev 使用 `water-dev`。配置根目录优先使用绝对路径 `$XDG_CONFIG_HOME`，其次兼容 `$XDG_CONFIG`，否则使用 `~/.config`；相对路径环境值被忽略。GUI、TUI、控制命令和 server 使用一致的 `--config` > `WATER_CONFIG` > 默认路径优先级。缺失文件用默认值，格式错误使启动失败。

macOS 首次读取默认新路径时，按原始字节复制 `~/Library/Application Support/water[-dev]/config.json`，以 0600 权限原子发布且不覆盖现有目标；旧文件保留。迁移需要目标文件系统支持硬链接，失败会报告错误。运行中旧进程继续使用旧路径，升级后重新启动 GUI/server 才能统一路径；旧文件后续不会自动同步。Web 授权、recovery 等其他目录数据不随配置文件复制，Web 可能需要重新配对；回退旧版本时需自行同步后来保存的新配置。

设置顶部显示实际配置文件绝对路径，并支持打开其所在目录（目录不存在时创建）。恢复默认先修改客户端偏好草稿，保存后持久化；Agent/Web/Server 仍使用各自的设置流程。

UI 支持 `en`、`zh-Hans`；加载兼容 `zh`/`zh-CN`。语言预览同时影响原生菜单，取消恢复已保存值；终端内容及用户名称不翻译。

## Finder 文件夹服务

macOS app 声明 Finder 服务 `Open New Workspace in Water`，开发版名称为 `Open New Workspace in Water Dev`。安装应用并由系统注册服务后，可在 Finder 文件夹右键菜单的 Services 中调用；菜单是否显示也受 macOS 服务启用状态和缓存影响。

服务读取调用方 pasteboard 的文件路径或 file URL，校验所选项目均为目录。每个目录分别创建一个新的本地 workspace，终端以该目录为工作目录；重复打开同一目录仍创建新 workspace，不向 shell 注入 `cd`。当前选中远程连接时切换至本地连接。多选目录按完整请求放入有界队列，队列满时返回服务错误。

原生服务 provider 在 GUI 初始化时注册，保持 GLFW 的 application delegate。尚未验证 Finder 实际菜单点击、冷启动投递和多个 GUI 进程同时运行时的系统目标窗口选择；这些行为不能由构建、plist 校验或普通 GUI 冒烟测试推断。

## 启动与验证

使用 `go.mod` 指定的 Go 版本。macOS 原生构建需要 Xcode Command Line Tools；Linux 需要原生图形依赖与 display/GPU。裸构建的身份限制见 [构建打包与发布](build-and-release.md)。

```sh
go run ./cmd/water
# 独立测试实例：显式配置和 socket
water --config /tmp/water-test/config.json --control-socket /tmp/water-test.sock
# 保留 workspace、不创建初始终端；仅测试使用
water --no-initial-terminal
# 空模型；仅测试使用
water --empty-workspace
water --ssh build-box
```

普通启动、tab、split 使用真实配置 shell：优先 shell.program，再 WATER_SHELL、用户 SHELL（远端为远端账户），最后做 zsh/sh 发现。Zsh 启动集成不修改用户 startup 文件，显式 `-f` 保持隔离行为。

验证入口为 `scripts/go-ui-settings-smoke.py`、`go-ui-parity-smoke.py`、`go-ui-language-grid-smoke.py`、`go-ui-lifecycle-smoke.py` 和 `go-ui-multiwindow-smoke.py`；按 [测试与验证](testing.md) 选择匹配范围。

## 当前限制

- stale 视图恢复、几何、颜色和光标同步先尝试取得 pane 解析锁，忙时保留旧状态由后续 layout 重试；取得锁后仍同步制作快照。鼠标报告、滚动、选择和复制回调遇到忙 pane 时直接返回，不是全部排队重试；复制未取得文本时保留原剪贴板。不能保证洪泛 pane 的每次交互都被处理，也不能据此宣称全部帧路径无等待。后台快照构建已移出视图锁，使绘制和 pane 命中测试可以读取上一帧；构建仍持有解析/生命周期锁，不消除上述同步快照与忙时跳过交互的限制。
- GUI 必须有原生显示环境；交叉编译/headless 不证明窗口行为，隐藏测试不证明真实锁屏行为。
- 系统通知需要 bundle 身份及授权；拒绝权限时不崩溃不证明通知可见。完整 ZWJ emoji 与缺失字体覆盖也不能由位置测试证明。
- 模态滚轮拦截与剪贴板测试保全的已知边界分别见 [界面布局与交互](ui-design.md) 和 [测试与验证](testing.md)。
