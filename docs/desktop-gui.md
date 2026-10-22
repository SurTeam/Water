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

Cmd+C 只复制，无选区不发送字节；Ctrl+C 才是中断，Command/Super 不隐式编码成 Control。Cmd+D 不隐式发送 EOF。文本粘贴走终端 bracketed-paste 状态；macOS 图片粘贴交给前台 Agent 的图像读取路径，不自动上传本地剪贴板到远端。

IME composition/caret 使用统一坐标变换，平台缩放只应用一次。控制 API 注入用于验证共享处理器，真实系统键盘、IME 候选窗口仍需人工验收。

字体按配置的逗号分隔 family 顺序逐字形 fallback；字体元数据和 collection index 由实际字体文件读取。普通 Unicode 保留字体原点与共同基线，emoji/私用图标走专用适配；字形与选择的列宽语义统一由 [终端输出与渲染](terminal-rendering.md) 维护。

## Settings 与配置生效

Settings 是客户端草稿，保存成功更新基线并保持面板打开；重连保留草稿。Cancel、Escape 和设置快捷键退出脏草稿时需要明确丢弃；关闭整个窗口或 Quit GUI 不走同一草稿确认流程。异步结果必须核验当前 pane、connection 与面板生命周期。

| 配置范围 | 当前生效方式 |
| --- | --- |
| 语言、主题、字体、快捷键、cursor_style/cursor_blink 等 | 按字段实时投影；应用可用终端序列覆盖光标偏好 |
| startup.window_columns/window_rows 初始网格 | 用于新窗口计算像素与默认网格，不重设已有窗口 |
| startup.window_min_columns/window_min_rows 最小网格 | Settings 标注 New windows；当前已有窗口在后续边框拖动时也读取新最小值，保存瞬间不自动调整窗口 |
| shell/server 与已有 PTY 生命周期相关选项 | 不改变已经启动的进程；按相应字段使用新终端或重启 server |
| GUI Settings 的 Agent/Web/Server 操作 | 使用所选 connection 的服务配置，不能替代成本地外观保存 |

完整字段见 [config.example.json](../config.example.json)。macOS release 默认 `~/Library/Application Support/water/config.json`，dev 使用 `water-dev`；已有 XDG 配置可被选用。`--config` 优先于 `WATER_CONFIG`。缺失文件用默认值，格式错误使启动失败。

UI 支持 `en`、`zh-Hans`；加载兼容 `zh`/`zh-CN`。语言预览同时影响原生菜单，取消恢复已保存值；终端内容及用户名称不翻译。

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

- 恢复 stale 终端视图时，`ensureTerminalView` 仍同步调用 `FrameSnapshot`，可能等待解析锁；帧线程完全不等待解析尚不是普遍保证。
- GUI 必须有原生显示环境；交叉编译/headless 不证明窗口行为，隐藏测试不证明真实锁屏行为。
- 系统通知需要 bundle 身份及授权；拒绝权限时不崩溃不证明通知可见。完整 ZWJ emoji 与缺失字体覆盖也不能由位置测试证明。
- 模态滚轮拦截与剪贴板测试保全的已知边界分别见 [界面布局与交互](ui-design.md) 和 [测试与验证](testing.md)。
