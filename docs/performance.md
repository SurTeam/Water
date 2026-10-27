# 性能与资源管理

性能优化同时受输出正确性、交互响应和资源所有权约束。实现入口为 `internal/goui/workspace.go`、`ebiten_pacing.go`、`internal/goclient/client.go`、`internal/govt/`、`internal/goterminal/`；字体依赖见 [WATER_CHANGES](../third_party/typesetting/WATER_CHANGES.md)。

## 设计原则

1. 减少重复工作，不靠丢原始输出、损坏历史或人为延迟输入换取低 CPU。局部基准改善需要整条路径独立验收。
2. 解析、传输和呈现分别调度。线程间共享不可变行，合并不可见中间画面时仍完整处理原始字节与终端回复。
3. 每层队列和缓存明确归属、容量、溢出与取消语义。单一洪泛终端不能阻塞全局控制；关闭和取消应解除等待。
4. 资源预算按实际所有者计算，Go 堆、GPU、原生分配与 replay 分别观察；软预算不是进程内存硬上限。

## 当前调度与队列逻辑

GUI 窗口使用可变期限 timer：空闲时每 250ms 进行维护检查，仅在聚焦且可见窗口的活动终端光标可见并启用闪烁时追加相位期限（600ms 显示、400ms 隐藏）。隐藏、非活动或滚出视口的光标不驱动闪烁重绘。worker invalidation 立即唤醒；排队输入、持续按键或未呈现 revision 保留约 60Hz 重试，光标闪烁资格改变后重新设置期限。窗口失焦时绘制稳定的非活动光标。

该策略减少空闲定时唤醒，不保证持续输出 CPU 或 macOS Energy Impact 按固定比例下降。未聚焦隔离窗口的短时 CPU 对照不能替代聚焦交互、长期能耗和系统标签验收；恢复延迟及 Metal 稳定性仍需独立检查。

可见终端的首次输出变化立即请求后台快照发布，持续输出在显示消费和 timer 协调下最多约 60 次/秒发布；它会合并中间画面，持续流中的回显也可能等到下一次发布。输入活动和窗口显示同步独立，空闲保留已有画面。后台快照发布先取得解析/生命周期锁，再取得视图锁，避免等待解析时占用视图锁；跟随输入、滚动与快照替换仍由视图锁串行化，快照构建本身仍可能阻塞视图读取。隐藏终端继续解析但不持续制作可见快照。恢复、几何与样式同步先尝试取得该 pane 的解析/生命周期锁，忙时保留旧状态并由后续 layout 重试；成功后仍同步构建视图，不能据此保证所有 GUI 路径完全无等待。具体交互边界见 [Go 桌面界面与配置](desktop-gui.md#当前限制)。

GUI 正常 socket 路径按 terminal 实例拥有独立订阅连接和解析 worker，直接消费该连接容量为 8 的事件队列；满队列只对所属终端连接施加背压，不占用控制 RPC 或其他 pane 的消费循环，也不把未解析的 live 字节丢弃后用历史 replay 代替。订阅在 attach 前建立，按句柄取消，旧 attach 的取消不能关闭同 UUID 的新订阅。TUI/legacy socket 路径仍通过独立终端连接转发到容量为 64 的聚合队列；浏览器复用 WebSocket。服务端 subscriber 队列容量为 64，满时等待容量或取消。

GUI 的兼容/in-memory 聚合事件路径另有每终端 256 events / 1 MiB mailbox，超限显式停止该终端输出并报告错误。初始 attach/replay 在后台执行，失败最多尝试四次，退避为 100/200/400ms；取消可中断连接与 RPC。实时输出和 replay 按 4 KiB 块解析，块间检查关闭；正在执行的一次图形解析仍需自行返回。关闭立即取消订阅并清空待处理队列，worker 随后释放 emulator。

纯 `terminal.send_text` / `terminal.send_bytes` 成功后不广播 workspace 模型快照，输入回显继续由终端输出流传递；命令结果与 operation 查询保留。

普通 session send 队列为 64 frames，并受 `MaxFrameBytes` 对应字节预算约束。终端输入/resize 先进入 staging，合并相邻同终端同类型操作，再分块送往 socket 队列；staged bytes 溢出返回背压错误但保留 session。staging 数量预算与错误呈现的限制见下表。

## 内存与历史逻辑

终端绘制在行数、索引和内容 hash 均未变化时直接保留纹理和 prepared-row 缓存，避免光标或其他局部重绘重复建立行匹配表；滚动、内容变化和几何/样式失效继续走原有匹配与重建路径。macOS 内部可见性检查直接读取不可变发布状态，不复制控制 API 的状态表；按键重复参数初始化后复用，焦点重置后重新读取。这些减少局部工作，不代表已证明整体 CPU 或系统能耗下降。

字体按需解析并有界缓存，emoji 按字号选择 bitmap strike；可见行和 PTY 读取块复用不得改变历史可访问范围。进程内 PTY 与 supervisor 均在 reader 内将内核碎片聚合进 128 KiB 块，最多缓存两个备用块；暂无数据时交给 runtime poller 等待，部分块按 1 ms 空闲 / 5 ms 最大窗口刷出。supervisor 消费端将块复制到 pending 后才归还，发布数据不能引用已复用的读取块。

supervisor 的原始历史使用最大 2 MiB 的环形缓冲，满后仅覆盖新字节对应的区域；attach 时按绝对 offset 复制为独立、有序的 replay。历史快照与订阅登记在同一锁区间建立边界。

GUI Go 堆软预算至少 192 MiB，并给存活数据保留 64 MiB 余量；显式 `GOMEMLIMIT` 优先。server/CLI 不套用 GUI 预算，native/GPU 分配不在其中。

server replay 扫描 escape 以识别主副屏边界与不完整序列，普通 escape 和相邻文本按连续原始 span 写入，避免逐 escape 重复统计与追加；跨块未完成序列仍保留到下一块或事件屏障。主屏压缩资格判断使用增量片段元数据，escape 裁剪使用单调游标，减少重复扫描原始字节；每次 publish 仍遍历保留事件重算 replay accounting。超限副本不得持有超大输入；原样返回切片可能 alias，其所有权不能被假设为独立。主屏按行保留与单帧传输限制见 [终端输出与渲染](terminal-rendering.md)。

## 静态审查确认的边界

| 路径 | 当前事实与需要验证的风险 |
| --- | --- |
| staging | 有输入字节预算，无完整事件数量预算；交错 resize 等不能视为已受 64-frame socket 队列总量约束 |
| GUI 输入拒绝 | `workspace.go` 的输入回调忽略 `DispatchAsync` 返回值，不能保证溢出拒绝对用户可见 |
| subscriber 关闭 | 满队列 publish 可被订阅取消或 terminal 关闭解除；有容量时优先发送最终事件，已关闭慢消费者不保证收到全部积压或 Exit |
| 恢复隐藏视图 | TryLock 忙时保留旧视图并延后恢复；取得锁后仍同步构建 FrameSnapshot |

staging 与输入拒绝仍是源码可见缺口，不能将设计目标写作已全面满足。pane 隔离、繁忙恢复与满队列关闭已有专门回归；验收范围见 [测试与验证](testing.md)。

## 测量与验收

比较同一输入、历史、几何、字体、可见性、构建和窗口时长，记录实际吞吐、CPU、分配、画面节奏及最终输出一致性。没有负载定义，不承诺固定 CPU 百分比、RSS 上限或跨机器延迟。

CPU 为进程 CPU 时间差/墙钟时间差，一个核 100%。累计分配、GC 后存活堆、RSS、physical footprint 和 GPU 内存分别报告；不能相减推断对象归属，也不能把分配降幅写成常驻内存降幅。阶段墙钟计时可能含等待且互相包含，不能相加充当 CPU。

control 往返包含 CLI/transport，Draw 提交不是硬件上屏；物理输入/IME 单独验收。短期稳定与 GC 回落不能证明长期无泄漏，后续通过不能消除未定位失败。

## 诊断入口

```sh
go build -tags water_cpu_diagnostic -o /tmp/water-profile ./cmd/water
~/.venv/bin/python scripts/diagnose-gui-cpu.py --water /tmp/water-profile --memory --sustained-seconds 40
~/.venv/bin/python scripts/diagnose-gui-input.py --water /tmp/water-profile --timings --seconds 25 --rate-mib 4
~/.venv/bin/python scripts/diagnose-gui-cadence.py --water /tmp/water-profile
```

这些脚本隔离 socket/config/进程；诊断端点仅显式 tag 构建存在。`scripts/diagnose-gui-stalls.py` 只读观察指定 GUI，在慢请求时采样；一次未捕获不排除间歇卡顿。`scripts/diagnose-server-cpu.py` 做同录制输入对照，录制内容仅保存在私有目录。

`water ctl debug memory` 分别报告 Go 堆与 replay；`terminal_count` 是 active PTY，`exited_terminal_count`/`exited_replay_bytes` 是退出缓存。释放检查须覆盖 reader 结束、取消订阅、退出缓存淘汰和引用移除。证据与进程所有权见 [测试与验证](testing.md)。
