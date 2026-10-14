# water-server 内存排查与压力测试（2026-10-14）

## 已确认的原因与范围

本次可复现的主要分配热点是 `internal/goterminal/replay_history.go` 的
`capEscapeBytes → completeSpans`。每次裁剪都会为所有普通字节段和 ANSI
序列建立索引数组。密集 TUI 输出不断重复建立这张表，形成很大的临时分配和 GC
负担。累计分配量不是当前存活堆，也不能直接换算为 RSS。

40 个真实 PTY 的原始压力测试通过，累计分配 5861.91 MB，索引数组的 **flat 与
cumulative 均为 5011.73 MB（85.50%）**。原实现的数组 append 位于当时源码
396、404 行。该结果支持优化分配热点，不能据此声称找到了运行中的 release
进程全部存活对象或修复了泄漏。

另一个分配路径是主屏历史超过 screenReplayLimit 且包含 ESC 后，
`shrinkPrimaryRedrawsLocked` 会汇总历史并扫描，即使最终没有需要裁剪的行。
本次只消除已测量的 span 索引数组，保留现有历史策略。

## 资源释放证据

增强后的同一 fixture 在原实现与优化实现下都采集两次 GC 后的 MemStats 和可选
inuse profile。所有测试仅操作独立测试实例，未重启、发送信号或修改用户 pid 4266。

| 40 PTY 状态 | 原实现 HeapAlloc（字节） | 优化后 HeapAlloc（字节） |
| --- | ---: | ---: |
| baseline | 306736 | 306056 |
| 终端存活，洪峰完成 | 18099936 | 18106392 |
| CloseAll、等待所有 readerDone、清除测试自身引用 | 1268768 | 1105352 |

两次存活 replay 计数均为 1272920 字节；goroutine 从 2 到 122，再回到 2。
因此本次优化没有宣称降低存活 replay 或静态堆。inuse profile 的主要应用分配栈
是 `Terminal.rawReadLoop`，关闭后该栈消失。源码使用 128 KiB 读取块，空闲缓存
最多两个块，阻塞读取还持有一个块；40 个终端的这部分正常缓存约 15 MiB。
pprof 默认采样会估计字节数，不能将其估计值当成精确 MemStats 分区。

退出缓存测试每轮创建并退出 12 个终端，每个写 2 MiB 无 ESC 的原始内容，
共 6 轮、72 次退出。每轮缓存都为 3 个终端、6291840 字节（含事件开销），
满足已有 8 MiB/32 个终端预算。优化后的两次 GC HeapAlloc：

| 状态 | HeapAlloc（字节） |
| --- | ---: |
| baseline | 287632 |
| 第 1 轮 | 6592096 |
| 第 2 轮 | 6597560 |
| 第 3 轮 | 6597544 |
| 第 4 轮 | 6597576 |
| 第 5 轮 | 6597688 |
| 第 6 轮 | 6597816 |
| CloseAll 两次后 | 304904 |

profile 将该有界缓存定位到 `writePrimaryLocked`。清空后缓存对象消失。
1 MiB 增长阈值是回归报警，不能证明任意长时间运行都没有泄漏；每一轮数据均
记录，缓慢但重复的上升趋势仍需进一步扩大周期并检查对象所有者。

`Subscribe` 的取消函数通过 `sync.Once` 删除 subscriber；server attach 流中的
`defer cancel` 负责结束订阅。完整 terminal 回归覆盖服务器背压和流隔离。
这些证据未显示上述路径持续泄漏，但不等于对所有 websocket 工作负载作泄漏证明。

## 代码优化与契约

将索引数组改为两个单调向前的字节游标：一个扫描完整片段的结束位置，另一个
在窗口超过限制时移走最旧的完整片段。每个片段最多解析两次，时间复杂度 O(n)，
选择窗口仅使用 O(1) 空间；需要返回缩短结果时只分配一份后缀。

保留现有语义：

- 完整且不超过 limit 的输入返回原切片；短切片仍可能引用较大的 backing array，
  这是既有所有权契约，未声称本次将所有 retained capacity 都限制到 limit。
- 超限或尾部不完整时返回独立副本，不能继续引用超大输入缓冲区。
- 丢弃尾部未完成的 CSI/OSC，保留完整 escape 边界和原有 oversized span 规则。
- 不改行数预算，不恢复已移除的全局字节上限，不裁剪 CR 后完整后缀或普通长行。
- CSI 在 successive read 间由 scanHold 保存；OSC 在 cap 边界可能丢弃未完成前缀，
  后续 terminator 不恢复它。新增测试明确记录这一既有行为。

测试包括冻结的旧算法作为 reference、3000 组确定性随机输入、异常 CSI/OSC、
负数与零 limit、nil/empty、超长单片段、输入不被修改、保留短输入 alias、
超限输出独立存储，以及生产 publish 路径的所有 read split 边界。

## 同 fixture 的前后结果

为了避免把不同测试集合直接比较，增强的压力/退出缓存 fixture 在同一测试源码下
分别使用 HEAD 原算法与优化算法运行，采样率均为默认 524288 字节：

| alloc_space | 原算法 | 优化后 |
| --- | ---: | ---: |
| 全测试累计分配 | 4282.42 MB | 1199.00 MB |
| completeSpans flat | 3187.25 MB | 无该函数/索引数组分配 |

这一组累计分配约减少 **72.0%**。PTY 调度和 read batching 会改变具体分配总数，
不将单组运行的差值当作固定产品收益。

配对 benchmark 在同一个二进制中比较 reference 与 constant 实现，3 次重复，
每组 100ms；输入为 4 KiB、32 KiB、256 KiB、1 MiB，普通文本、密集 CSI、
混合 FRAME/CSI/OSC，limit 分别为输入长度的一半、相等、两倍。
36 组配对中未观察到中位 CPU 耗时回退超过 10%。dense/mixed 输入按目标字节数截断，
部分样本故意以不完整 escape 结束；因此 limit 等于输入长度时也可能需要返回副本。
另有独立 allocation 测试覆盖完整输入的零分配路径。例如：

| 用例 | 原 ns/op | 新 ns/op | 原 B/op / allocs | 新 B/op / allocs |
| --- | ---: | ---: | ---: | ---: |
| 256 KiB dense，limit 128 KiB | 1078536 | 421484 | 7189812 / 29 | 131072 / 1 |
| 1 MiB mixed，limit 512 KiB | 2623980 | 1698773 | 18486543 / 31 | 524288 / 1 |

完整、limit 内的 262143 字节 CSI 输入还通过 zero-allocation 断言；被裁剪的
输入最多一次分配。未测量 peak HeapAlloc 或部署前后 RSS，故不报告其下降。

## 可重复压力测试方案

```sh
# 配对基准：结果保留为证据，比较 ns/op、B/op、allocs/op
 go test ./internal/goterminal -run '^$' \
   -bench '^BenchmarkCapEscapeBytes$' -benchmem -benchtime=100ms -count=3 -timeout=55s

# 独立真实 PTY 压力、两次 GC、每轮退出缓存及关闭清理
 WATER_TEST_MEMORY_PROFILE_DIR=/tmp/water-memory-evidence \
 go test ./internal/goterminal \
   -run 'TestDozensOfTerminalsKeepScrollbackUnderTUIPressure|TestExitedReplayHeapPlateaus' \
   -count=1 -timeout=55s -v -memprofile=/tmp/water-memory-total.pprof

 go tool pprof -top -alloc_space /tmp/water-memory-total.pprof
 go tool pprof -top -inuse_space /tmp/water-memory-evidence/pty-live.pprof
 go tool pprof -top -inuse_space /tmp/water-memory-evidence/pty-cleared.pprof

# 仓库完整 terminal profile：带 PID、日志、退出码、GUI/scenario 证据与清理
 ~/.venv/bin/python scripts/run-go-regression.py --profile terminal
```

接受条件：输出/所有权契约一致，span table 分配消失，benchmark 没有未解释的
大输入 CPU 回退；40 PTY replay <=4 MiB、post-GC heap 增量 <=24 MiB，关闭后
堆回到 baseline+4 MiB 内，reader 全部结束；退出缓存各轮有界、无持续明显增长。

## 回归验证与保存的证据

`~/.venv/bin/python scripts/run-go-regression.py --profile terminal` 已全套通过：
全仓库 vet/unit、相关包 race、GUI/server 构建、4 个 scenario，以及真实 GUI
pane/settings/selection/content/server/long-run/agent/query/graphics/Pi redraw 验证。
测试记录确认原有 Water 的 pid 4266、57081 保持运行。

证据目录为 `target/test-runs/20261014T020306Z-e492843c`；其中 `memory/` 保存
前后累计 allocation profile、baseline/live/cleared 和每轮退出缓存 inuse profile、
原始测量日志与 3 轮基准结果。采样环境：Go 1.27.2、darwin/arm64、CGO_ENABLED=1、
GOFLAGS 空；baseline revision 为 `a8787c14f826b2170f02cebd3b7d486ab271593b`。
对照运行使用同一增强测试源码，只替换生产 `replay_history.go` 为该 revision
版本，结束后恢复优化实现。所有这些文件属于本机测试证据，不是安装包。

## 尚未完成的实时归因

运行中的 release 没有取得 inuse heap profile。此前约 30–33 MB replay 与约
84–97 MB HeapAlloc 的差值未被分配到具体对象，也不能从 RSS、physical footprint、
vmmap RESIDENT 或 HeapReleased 相减而得到泄漏归因。此前将 RSS 解释为
Go heap 加 MADV_FREE 的说法已撤回。多个采样来自依次读取，不能称为原子快照。

下一步实时归因需要对可配置 profiling 的独立 server 重放实际工作负载，包括
长换行历史、多次 attach/detach、慢读客户端、关闭 pane 和 TUI 进出；在 warm-up、
各轮运行及所有连接/终端清理后使用相同两次 GC/profile 序列，再检查对象归属。
本次已完成可复现分配优化和隔离生命周期验证，但没有声称修复了用户 release
进程全部内存占用，也没有部署或重启该进程。
