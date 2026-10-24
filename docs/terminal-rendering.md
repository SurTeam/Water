# 终端输出与渲染

`internal/goterminal/` 管 PTY 与原始 replay，`internal/goclient/` 管传输，`internal/govt/` 包装 `internal/xterm/` 模拟器，`internal/goui/workspace.go` 和终端绘制模块负责桌面视图。TUI 与浏览器有各自模拟和呈现限制。

## 设计原则

- 服务端保存模型、PTY 元数据和受保留策略约束的原始 replay；客户端拥有模拟、scrollback、选区、图形与终端报告。
- Output、Resize、Exit 应保持同一有序 sequence；replay/live 接合去重并处理 gap。压缩不得跨事件屏障破坏重建几何，不得静默丢原始流。
- 历史重放不触发剪贴板、历史查询回复和铃声通知。解析与发布分离，旧帧引用的 cells 保持不可变。
- 绘制、命中、高亮、复制与光标共用逻辑网格和滚动偏移。缓存范围不得成为实际历史上限。快照发布必须与 GUI 滚动和跟随输入保持顺序，不能用较早构建的快照覆盖较新的 viewport；等待解析/生命周期锁时不占用视图锁。

## 当前输出与历史逻辑

PTY reader 发布有序事件，waitLoop 等待读取完成后发布 Exit。attach 先建立订阅再取得 replay，客户端按 sequence 跳过重叠；gap 重建依各前端实现。队列满时 decoded/subscriber 等待容量，发送拒绝与生命周期边界见 [性能与资源管理](performance.md)。

主屏按配置 scrollback 行数加 viewport 的原始换行保留，普通长行可完整保留，因而不是固定字节硬上限。含 escape 的超大重绘片段及活动 alternate buffer 使用屏幕预算；alternate 输出单独管理，离开后恢复主屏 replay。

自然退出缓存最多 32 个终端、8 MiB replay accounting，按最旧退出淘汰；显式关闭移除对象。被淘汰 ID 的查询失败，不能承诺退出历史无限可查。

## 清屏与 Agent 重绘

默认客户端可将 CSI 2 J 清屏内容保留为历史。普通 shell 的 CSI 3 J 清除历史；启用对应 Agent clear policy 时，模拟器用首次同步输出边界保留 shell 前缀，并替换 Agent 自身重绘历史。边界随客户端裁剪和 reflow 更新。

此策略作用于客户端模拟器的历史，隐藏终端也继续应用；它不改变服务端原始 replay 保留策略。服务端仍按原始换行裁剪，attach 只能重建尚在 replay 中的字节；不能保证 live 客户端保留的 shell 前缀在多轮重绘后重连仍存在。

## 网格、字形与选择规则

ANSI parser 的普通 ASCII run 批量写入 cells，保留前景/背景、宽字符边界清理、延迟换行与滚动区语义。charset、insert mode、关闭 wraparound、screen-reader 和 extended attrs 使用通用路径；非 ASCII 继续逐字符处理。批量路径须与通用路径对照验证全部保留历史、cell 属性、wrapped 标记与光标状态，不能只比较当前可见文本。

选择端点使用字符格中线和插入边界，正反向一致；Shift 点击保留锚点，双击词选不被松开位置覆盖。宽字符高亮与复制覆盖完整字符，历史裁剪使选区失效时清除，不能复制另一行。

模拟器列宽是布局依据。普通 Unicode 保留字体原点、左右留白与共同基线；emoji/私用图标按专用规则适配，不能将全部双宽字符按墨迹居中。私用图标可借同背景后续空格绘制，但不额外占逻辑列。框线、块、盲文和 sextant 依物理 cell 几何绘制。

滚动上限来自实际保留历史。改变 overscan、缓存、行复用或裁剪时，必须覆盖最早历史、继续输出、历史选择与复制；字体修改覆盖 CJK、组合字符、emoji、图标、换行和双向选择。实现入口：`internal/govt/selection.go`、`internal/goui/terminal_glyph_fit.go`、`selection_boundary_test.go`、`terminal_scroll_test.go`。

## 当前限制与静态风险

- replay 作为单个 JSON attach 响应，受 16 MiB frame 上限约束；主屏按行保留可能产生超大响应，长行/大历史不能保证重建成功。
- `shrinkPrimaryRedrawsLocked` 收集全部 Output 再替换，跨 Resize 保留非 Output 事件时可能产生 sequence 次序问题；尚未运行专门复现，不能将目标有序性宣称为所有压缩路径的已验证保证。
- 服务端原始裁剪不等同于客户端 Agent 历史语义，重连恢复范围只限保留 replay。
- 满 subscriber 的取消与关闭风险见 [性能与资源管理](performance.md#静态审查确认的边界)。

## 终端报告与剪贴板

以下报告由客户端通过真实 PTY 回复：

| Sequence | Behavior |
| --- | --- |
| CSI 18 t | Current terminal rows and columns, including after a split/resize |
| CSI 14 t / 14;2 t | Text grid / complete native window size in physical pixels |
| CSI 15 t / 16 t / 19 t | Monitor pixels / character-cell pixels / monitor capacity in cells |
| CSI 11 t / 13 t / 13;2 t | Window state / window position / text grid position |
| CSI 5 n / 6 n / ?6 n | Terminal status and current cursor position; honors origin mode |
| CSI 20 t / 21 t | OSC icon label / window title replies; title stacks use CSI 22/23 t |
| CSI ?996 n / ?2031 h/l | Light/dark query and configured-theme change notifications |
| DCS $q ... ST | Current SGR attributes, cursor style, protection and scroll margins |
| DCS +q ... ST | Hex-encoded terminal-name, 256-color and direct-RGB capability queries |
| OSC 7 ; file URI ST | Current directory metadata, exposed per grid by `water ctl ui snapshot` |
| OSC 52 ; c ; base64 ST | Copy UTF-8 text to the system clipboard; empty data clears it |

OSC52 writes are bounded to 1 MiB and coalesced; replay does not touch the
clipboard. Clipboard read queries receive an empty reply. Window/screen reports
use the GUI owning the pane, not guessed server-side dimensions. Unsupported
terminal capabilities return a negative report. Responses are bounded and replay
suppresses them. BEL and ST terminators and output split across chunks are tested.
Run `~/.venv/bin/python scripts/go-ui-query-smoke.py` on macOS for native checks:
it compares 18 actual PTY replies with GUI metrics before and after a split,
checks OSC7 and OSC52, and restores the previous clipboard text. Only its own
temporary GUI/server are closed.


## 图像与粘贴

Water displays static Kitty RGB/RGBA/PNG streams (including compressed chunks and
Unicode placeholders), iTerm2 inline images and Sixel. Images follow their buffer's
history, retain their cell height when partially visible, and disappear when their
last covered row expires. Kitty screen deletion preserves history; `kitten icat
--clear-all` also removes historical images. `kitten icat` negotiates stream transfer;
shared-memory and temporary-file transfer are not advertised. Advanced Kitty
animation and relative placements are not implemented.

On macOS, the configured paste shortcut recognizes native PNG, TIFF and other image
clipboard types and sends Ctrl+V to the foreground agent, preserving the clipboard
for its own image reader. This is verified with Codex and Claude Code. Text uses
the usual bracketed-paste path. Remote agents need their own clipboard integration;
the local clipboard is not uploaded automatically.

Zsh startup installs a one-shot forward-delete binding only when the key is
unbound, without changing user startup files. Explicit `-f` and command-mode
shells retain their isolated startup behavior.

```sh
go build -o target/go-ui-smoke/water ./cmd/water
~/.venv/bin/python scripts/go-ui-graphics-history-smoke.py
WATER_TEST_AGENT='codex --no-alt-screen' \
  ~/.venv/bin/python scripts/go-ui-graphics-history-smoke.py
WATER_TEST_AGENT=claude ~/.venv/bin/python scripts/go-ui-graphics-history-smoke.py
```

The macOS smoke preserves the native clipboard, creates an owned GUI/server with
a unique socket/config, and saves screenshots. Set `WATER_TEST_IMAGE` to override
the sample JPEG path. Agent verification attaches the image without submitting a
model request. Kitty protocol details are defined in its [graphics specification](https://sw.kovidgoyal.net/kitty/graphics-protocol/).

## 验证要求

使用 [测试与验证](testing.md) 的 terminal profile。流变更验证 replay/live、Resize/Exit、慢消费者与关闭；清屏验证 live 与服务端裁剪后重连，不能仅向 emulator 输入未裁剪 fixture。真实 GUI 内容用 ui content，Draw 用截图；TUI/Web 按各自能力验收。

## Agent 命令执行的屏幕范围

`pane.execute` 复用原始 replay 和订阅流临时重建模拟器，不持久化服务端逐屏快照，也不读取或修改 GUI viewport。唯一 OSC 生命周期标记确定执行边界，输出按 sequence 去重；范围使用保留行与裁剪计数定位。resize/reflow、备用屏幕切换及部分光标定位使范围失效时，返回有界活动屏幕并标记 `truncated`；重绘覆盖的文字不能作为日志恢复。具体参数、子 shell 和超时进程组保护见 [控制 API](control-api.md#在指定-pane-执行命令)。修改此路径须覆盖真实 PTY 的范围隔离、重绘、裁剪/上限、resize、备用屏幕、TERM/KILL、后代清理与 pane shell 存活。

## Supervisor 历史裁剪与重连模式

Supervisor 的 2 MiB 原始历史环在丢弃字节时推进有界的输入模式和解析状态检查点。历史已裁剪的 attach 单独携带恢复字节，server 在消费 replay 前发布它们；恢复字节不计入 supervisor 原始输出 offset。检查点保留鼠标跟踪/编码、备用屏幕及相关有限输入模式，并处理边界处拆开的控制序列。它不恢复已丢弃的屏幕内容。Attach 通过 `restore_supported` 区分支持检查点的 supervisor 与旧版本；支持检查点时空恢复字节表示模式已关闭，不能用旧状态覆盖。

GUI 在 server 重启的 recovery layout 中保存已知鼠标跟踪/编码及备用屏幕模式。仅在旧 supervisor、原进程被恢复、历史已裁剪且未提供检查点时，server 在 replay 前补入这个有限模式种子；保留输出中的模式重置和 live 输出仍优先。恢复字段限制为有界模式白名单，不接受任意终端输出。该兼容路径依赖重启前 GUI 仍知道正确模式；已经丢失的状态不能凭空恢复，GUI 快照之后又被旧 supervisor 丢弃的模式变化也无法准确重建。

Attach 响应和完整 replay 必须在快照锁内先入队，再注册 live 订阅，保证响应、replay、live 的顺序；socket 写入仍在异步队列消费端执行。修改此路径须验证环绕/超大写入的 offset、控制序列跨边界、模式重置、实际模拟器的鼠标报告，以及并发 attach 的 replay/live 连续性。真实 server 重启接管后的滚轮验证使用 `scripts/go-ui-tui-reattach-scroll-smoke.py`，确认上下 SGR 滚轮实际到达 TUI。
