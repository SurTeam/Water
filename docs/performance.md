# 性能与资源管理

性能优化同时受输出正确性、交互响应和资源所有权约束。实现入口为 `internal/goui/workspace.go`、`ebiten_pacing.go`、`internal/goclient/client.go`、`internal/govt/`、`internal/goterminal/`；字体依赖见 [WATER_CHANGES](../third_party/typesetting/WATER_CHANGES.md)。

## 设计原则

1. 减少重复工作，不靠丢原始输出、损坏历史或人为延迟输入换取低 CPU。局部基准改善需要整条路径独立验收。
2. 解析、传输和呈现分别调度。线程间共享不可变行，合并不可见中间画面时仍完整处理原始字节与终端回复。
3. 每层队列和缓存明确归属、容量、溢出与取消语义。单一洪泛终端不能阻塞全局控制；关闭和取消应解除等待。
4. 资源预算按实际所有者计算，Go 堆、GPU、原生分配与 replay 分别观察；软预算不是进程内存硬上限。

## 当前调度与队列逻辑

可见终端的首次输出变化立即发布，持续输出在显示消费和 timer 协调下最多约 30 次/秒发布；它会合并中间画面，持续流中的回显也可能等到下一次发布。输入活动和窗口显示同步独立，空闲保留已有画面。隐藏终端继续解析但不持续制作可见快照，恢复时更新视图；当前恢复路径仍可能同步等待解析锁，见 [Go 桌面界面与配置](desktop-gui.md#当前限制)。

Go 桌面/TUI 客户端的正常 socket 路径将终端 bulk output 放在独立连接和 SSH channel，避免占用控制 RPC 回复通道；浏览器的 RPC、attach 与 live terminal 复用同一 WebSocket。客户端 decoded 队列聚合容量 64，每条独立流 8；服务端 subscriber 队列 64。decoded/subscriber 满时等待容量，以阻塞背压保持流完整，并非一律断开。

普通 session send 队列为 64 frames，并受 `MaxFrameBytes` 对应字节预算约束。终端输入/resize 先进入 staging，合并相邻同终端同类型操作，再分块送往 socket 队列；staged bytes 溢出返回背压错误但保留 session。staging 数量预算与错误呈现的限制见下表。

## 内存与历史逻辑

字体按需解析并有界缓存，emoji 按字号选择 bitmap strike；可见行和 PTY 读取块复用不得改变历史可访问范围。PTY 使用 128 KiB 读取块，最多缓存两个备用块；暂无数据时使用带期限的就绪等待。

GUI Go 堆软预算至少 192 MiB，并给存活数据保留 64 MiB 余量；显式 `GOMEMLIMIT` 优先。server/CLI 不套用 GUI 预算，native/GPU 分配不在其中。

主屏压缩资格判断使用增量片段元数据，escape 裁剪使用单调游标，减少重复扫描原始字节；每次 publish 仍遍历保留事件重算 replay accounting。超限副本不得持有超大输入；原样返回切片可能 alias，其所有权不能被假设为独立。主屏按行保留与单帧传输限制见 [终端输出与渲染](terminal-rendering.md)。

## 静态审查确认的边界

| 路径 | 当前事实与需要验证的风险 |
| --- | --- |
| staging | 有输入字节预算，无完整事件数量预算；交错 resize 等不能视为已受 64-frame socket 队列总量约束 |
| GUI 输入拒绝 | `workspace.go` 的输入回调忽略 `DispatchAsync` 返回值，不能保证溢出拒绝对用户可见 |
| subscriber 关闭 | publish 等待容量或订阅取消；Terminal.Close 本身未在此 select 中唤醒，满队列且不取消的消费者可能阻塞清理 |
| 恢复隐藏视图 | 同步 FrameSnapshot 可能等解析锁，尚未达到所有恢复均由后台发布的目标 |

这些为当前源码可见的缺口，尚未逐项运行专门复现。不能将设计目标写作已全面满足；修改相应路径时先复现并验证行为。

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
