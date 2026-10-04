# 高速输出的 CPU 管线测量（2026-10-04）

结论：本次已捕获正在运行的 Water.app，并对当前源码做阶段插桩、CPU/分配 profile 和原生 GUI 对照。**高速输出约 10% 尚未实现，也没有足够证据保证重写后一定达到。** 第一轮改动减少了重复画面和字符串构建；普通构建的短窗口中，1 MiB/s ANSI 输出从 38.90% 降到 19.99%，4 MiB/s 从 60.24% 降到 26.75%。较长连续输出的修改版窗口为 31.99%–36.18%，不能只采用短窗口的最好结果。

测量阶段没有替换 `/Applications/Water.app`；测试二进制保存在 `/tmp/water-cpu-investigation-20261004/`。上述第一轮改动随后纳入 0.3.4，发布记录见 [版本说明](releases/v0.3.4.md)。

## 当前用户实例

Apple M1 Pro，macOS 27.0，3024×1964 Retina 主显示器，Metal 4。正在运行的 release 为 0.3.2：GUI PID 66089，独立 server PID 66132。用户确认负载为 Agent 持续输出。

用 `ps` 的累计 CPU 时间差测量 30 秒，每 5 秒取一次；100% 表示占用一个 CPU 核，包含进程各线程：

| 进程 | 六个窗口的 CPU（%） | 30 秒均值 |
| --- | --- | ---: |
| Water GUI | 29.01、28.18、26.26、27.40、31.82、30.53 | 28.87% |
| Water server | 2.55、2.56、2.35、2.56、2.96、2.58 | 2.59% |

合计约 31.46%。这里只计 Water 进程，不含 Agent 自身和远端进程。原生 8 秒采样在 `/tmp/water-cpu-66089.sample.txt`。安装包的 Go 符号被剥离，因此读取 Mach-O 的 `__gopclntab`，以 `__text` 起始地址还原函数，确认采样包含 `Draw → layout → layoutNativePane → drawTerminal`、侧栏标签、文字绘制和 Metal 提交路径。原生 sample 中大量休眠线程的样本不能当作 CPU 占比。

用户实例的两个已发现 socket 均返回 connection refused，无法读取活动 pane、窗口状态或输出速率。本次没有因此重启或关闭用户实例。下面的阶段数据来自隔离实例，不冒充用户实际 Agent 的 profile。

## 测量方法

基线为本次开始时的源码 HEAD `ce1c110`。普通构建与 `water_cpu_diagnostic` 构建分开使用；dev GUI 原生构建，每次使用唯一 socket、临时配置、检测到的 zsh `-f`，server 嵌入该测试 GUI，结束只关闭测试 PID。因此隔离实例的进程 CPU 包含其嵌入式 server，与用户 GUI 单独的 28.87% 不能直接比较。

诊断构建新增：解析字节数；解析、快照、行匹配、行栅格绘制、合成和原生平台发布的次数及累计耗时；可见行、准备行、重绘行、文字 run 数；以及通用 Invalidate 的调用栈计数。只有诊断构建持有这些有界计数和计时窗口，正常构建不输出逐帧日志。

`/debug/latency?reset=1` 在启动和前置分配采集之后重置，`/debug/latency` 返回 dump；Go pprof 用于 CPU 和分配差值。阶段累计耗时是墙钟时间，会包含锁等待、调度和 GC 暂停；父子阶段存在包含关系，不能相加成 CPU 占比。CPU 的依据是进程计时和 CPU profile。

测量脚本同时修正了输出就绪条件：等待输出程序自身的 `WATER_OUTPUT_LOAD_START`，并记录 hidden/occluded。初次低到 3.86% 的“输出”结果没有获得有效持续绘制证据，弃用。最后补充了窗口时长和诊断构建的实际解析 MiB/s 字段。原始报告中的 `rate_mib` 是发生器目标上限，并非保证达到的输入速率。

## 哪一段最大，哪一段最重复

修改前的诊断窗口约 10 秒：

| 阶段或工作量 | 1 MiB/s 目标 | 4 MiB/s 目标 |
| --- | ---: | ---: |
| 实际解析字节 | 9,985,536 | 37,137,408 |
| 解析累计耗时 | 152 ms | 527 ms |
| 快照累计耗时 | 97 ms | 161 ms |
| 行栅格绘制累计耗时 | **1,422 ms** | **1,285 ms** |
| 行合成提交累计耗时 | 9 ms | 15 ms |
| 快照发布次数 | 591 | 639 |
| 重绘行数 | 8,288 | 8,958 |
| 窗口 Update 次数 | **1,167** | 639 |
| 原生平台属性发布次数 | 973 | 638 |

数据说明：这两组中，图像生成前的文字/行处理比 ANSI 解析耗时大；1 MiB/s 已经能驱动近 120 次/秒的窗口更新，实际上只有约 60 次/秒需要显示新的画面。合成调用本身的计时小，不表示异步 GPU 提交和原生后端成本为零。

1 MiB/s 的分配差值约 **605 MB/10 秒**：glyph 构建 146 MB、可见快照 143 MB、Harfbuzz shaping 63 MB、`prepareRow` 39.5 MB，另有 ObjC/反射及协议缓冲。CPU profile 的主要调用链包括文字绘制、`mallocgc`、内存清零、原生调用和线程唤醒。累计 CPU 调用链同样不能相加。

关键重复工作是：

1. 一个持续输出流同时驱动快照发布和显示器刷新率的输入循环。
2. 新行通常仅改变计数或一小段文本，却以整段字符串为单位重新构建 glyph/shaping 数据；已有行纹理缓存不能复用内容已变化的整行。
3. `prepareRow` 逐 cell 执行 `currentText.text += text`，反复分配、复制不断增长的前缀。
4. 每次更新重新读取原生菜单属性。当前窗口中它比行栅格绘制小，因此没有把它当作优先重写目标。

## 已做的第一轮改动及代价

客户端仍按 sequence 完整解析 Output、Resize、Exit，立即处理 VT 回复，不丢输出字节。第一次输出变化立即发布；持续输出等待上一份画面被消费后，最多以约 30 次/秒发布最新快照。等待在 worker 的可停止 timer 中进行，不阻塞 GUI 帧循环，也不让解析等待绘制。

纯输出通知只标记 revision 并唤醒画面，不再刷新“交互活动”时间；按键、鼠标、滚动、拖拽和 IME 仍保留各自输入路径。文字 run 的连接改用 `strings.Builder`，避免反复复制前缀，原有字体、ligature、emoji、宽字符和颜色分组规则不变。

**30 次/秒是明确的呈现代价**：高速输出跳过不可见的中间画面，显示最新完整状态；文字回显在持续流中也可能等到下一次快照发布。没有把整个窗口锁到 30 FPS，也没有通过降低输入/输出字节量获得低 CPU。这一轮不是整个渲染器重写。

## 对照结果与限制

普通构建、不运行 pprof、不强制 GC：

| 负载 | 基线 | 修改版 | 窗口 |
| --- | ---: | ---: | --- |
| 1 MiB/s ANSI 目标 | 38.90% | 19.99% | 各 10 秒 |
| 4 MiB/s ANSI 目标 | 60.24% | 26.75% | 各 8 秒 |
| 4 MiB/s 目标，20 秒连续输出 | 43.20%、41.17% | 35.79%、36.18%、31.99% | 连续的 5 秒窗口 |

连续基线第三个窗口为 40.19%，结束时 `window_occluded=true`，**不纳入可见输出对照**。桌面交互、窗口遮挡和 GC 状态会影响结果；脚本只在测量边界记录可见性，不能证明窗口全程未遮挡。基线/修改版长窗口不同时进行，不能据此给出严格的固定百分比收益，也不能承诺长时间一直保持短窗口的 26.75%。

修改版 4 MiB/s 诊断窗口实际解析了 35,059,200 字节，约 3.3 MiB/s：308 次快照、322 次呈现、398 次 Update、4,341 行重绘。解析累计 513 ms、快照 93 ms、行栅格绘制 597 ms。字节解析量仍高，重复绘制/更新减少；这不是只画画面而不消费输出。

该窗口分配约 377 MB：glyph 构建 88 MB、快照 70 MB、协议 ReadFrame 40 MB、shaping 37 MB、PTY readLoop 34 MB。CPU profile 中内存清零约 400 ms、系统调用约 320 ms、条件变量 signal/wait 约 250/240 ms、原生调用约 220 ms。不同窗口和采样误差存在，不能用这些数字反推出严格的单项可节约上限。

4 MiB/s 目标下，control API 输入处理往返：基线 95 次，P50 46.73 ms、P99 153.14 ms；修改版 96 次，P50 46.89 ms、P99 62.76 ms。包含 CLI 启动、transport 和处理，不是物理键盘到像素的延迟；也没有验证真实 OS IME 的全部行为。

## 10% 的可行性与下一次重写边界

CPU 成本应拆成：每字节解析/传输成本 × 字节率，加每快照成本 × 发布率，加变化文字的 shaping/栅格成本，加每帧提交/调度成本，加固定后台成本。提高字节率而 CPU 永远不变不可能；要讨论 10%，必须固定吞吐、终端尺寸、字体/Unicode 复杂度、pane 数和最低呈现频率。

当前实测没有证明 10%。4 MiB/s 目标下，仅解析的阶段墙钟时间就约占半个 CPU 秒/10 秒量级，加上后台和传输，需要显著减少其余开销，不能只再少画几帧。下一轮应按 profile 重写以下边界，并逐项独立对照：

- **文字管线**：保留 shaping 语义和字体 fallback，以有界 glyph atlas/可复用 glyph 几何批量提交，避免变化整行成为全新的大对象。ASCII 快路径必须与 ligature、CJK、combining、emoji 和字体特性验证配套，不能直接按字节切割字符串。
- **可见快照**：采用紧凑 cell/style 表或有明确生命周期的只读帧缓冲，减少大 `[]Cell` 的清零和复制。先证明 renderer 不会读取被 parser 覆写的数据，再引入复用；不能直接共享可变终端模型。
- **传输与 PTY**：基于实际 read/frame 次数与字节数验证有界缓冲复用和批次大小，减少重复分配、系统调用、线程切换；仍保留 replay/live 衔接、sequence 去重、背压和关闭上限。

每一项都要同时检查实际解析吞吐、CPU 时间、分配、画面间隔、最终屏幕状态和 VT 回复。更低呈现率可作为单独取舍实验，不能把它伪装成同等视觉体验下的管线效率提升。

## 验证及原始证据

通过 `gofmt`、`go vet ./...`、`go test -timeout=55s ./...`；GUI/VT/client 的 race 测试；诊断 tag 的 GUI 和命令模块测试；真实 GUI 高速输出、control 输入和截图。匹配 dev server 的隔离 scenario suite 中，workspace_basic、terminal_basic、agent_detection、terminal_zsh 全部通过。

真实 PTY query smoke 的修改版首次出现 CSI 19t 两秒回复超时，证据在 `/tmp/water-queries.4qwhqc21`。随后修改版连续五次通过，每次 36 个真实 PTY 查询及 OSC7/OSC52；基线也连续五次通过。**首次超时的原因未确定，不能把后续通过当作已定位修复。** 终端状态查询是即时 parser/dispatcher 路径，代码没有把回复放到 30 Hz 快照 timer 中，但仍需在更长压力验证中排除此类尾延迟。

关键原始目录（临时文件未提交 Git）：

- 1 MiB/s 基线普通构建：`/tmp/water-cpu.b7anr_vt/report.json`
- 1 MiB/s 修改版普通构建：`/tmp/water-cpu.w_jpuvht/report.json`
- 4 MiB/s 基线/修改版普通构建：`/tmp/water-cpu.wj49w6dp/`、`/tmp/water-cpu._cd_4841/`
- 连续输出基线/修改版：`/tmp/water-cpu.xon8y2vm/`、`/tmp/water-cpu.ubrw670b/`
- 修改前阶段 dump、CPU/alloc profile：`/tmp/water-cpu.ui2xaxy3/`（1 MiB/s）、`/tmp/water-cpu.o9wo4xde/`（4 MiB/s）
- 修改后阶段 dump、CPU/alloc profile：`/tmp/water-cpu.yhsgub9g/`（4 MiB/s）
- 输入基线/修改版：`/tmp/water-input.6iwo4614/`、`/tmp/water-input.ti4qqi2f/`

复现：

```sh
go build -tags water_cpu_diagnostic -o /tmp/water-pipeline-dev ./cmd/water
~/.venv/bin/python scripts/diagnose-gui-cpu.py \
  --water /tmp/water-pipeline-dev --profile --seconds 10 --rate-mib 4
go tool pprof -top /tmp/water-pipeline-dev /tmp/<报告目录>/ansi-output.pprof
go tool pprof -top -sample_index=alloc_space \
  -base=/tmp/<报告目录>/ansi-output-allocs-before.pprof \
  /tmp/water-pipeline-dev /tmp/<报告目录>/ansi-output-allocs-after.pprof
```

这些测量支持第一轮改动及下一轮重写依据，不表示已达到 10%；安装包的构建与签名验证独立于这里的性能测试。
