# GUI 内存与连续输出输入延迟验证（2026-10-04）

## 后续：画面交接与帧节奏重写

用户进一步报告长按字符成批出现、持续输出跳帧。“快照”的核心作用是让
解析线程与界面线程安全共享完整画面；截图、固定发布频率和整屏复制不是
必需的。直接读取可变终端会出现半更新状态，或让界面等待 ANSI 解析锁；
把解析搬到界面也会阻塞输入。

现在使用行版本与不可变可见视图：未变化的行共享存储，变化行重新生成，
缓存只保留当前可见区域。独立副本 `Snapshot()` API 保留，渲染使用
`FrameSnapshot()`。后台始终按顺序解析全部事件，首次变化立即发布；
绘制消费完当前视图后，通过有界 channel 请求最新视图。没有独立的快照
定时器，中间画面可合并，但不丢弃输入/原始输出，不人为逐字符延缓输出。
隐藏窗口暂停制作新画面，仍解析输出，显示恢复后请求最新视图。

活动期间跟随显示器同步，Update 每帧一次；空闲时保留画面并休眠。
导航/控制键按实际时间重复，不追赶积压的重复次数；普通字符仍使用系统
文本/IME 路径。ANSI 解析与视图读取使用不同的锁，关闭与解析单独串行。
字符尺寸未改变时，不再每帧进入解析器锁设置同一尺寸。

相同 80×24 ANSI 内容的基准：独立副本约 59 μs、151 KB/418 次分配；
不变行帧视图约 1 μs、3.2 KB/5 次分配，分配减少约 98%。已验证 500 次
终端变更后与独立副本逐项相同，以及旧行不可变、缓存有界、显示消费驱动
发布、关闭/解析并发、不同刷新率下控制键重复。完整 Go、vet、相关 race
和真实 GUI 设置回归通过。

最终 4 MiB/s、15 秒压力测试（不含结束验证阶段）：原生 Update P99
0.69 ms/最大 3.40 ms，Draw P99 4.14 ms/最大 6.61 ms，Update 间隔最大
16.93 ms。每次启动 CLI 的控制按键往返 P99 73.40 ms/最大 73.77 ms。
文件 `/tmp/water-input.bfsflhm1`。变化画面的 Draw 最大间隔仍为 45.61 ms，
与输出事件源约 47.78 ms 的最大间隔对应；没有新数据时会保留旧画面，
不人为造出中间字符或声称所有间隔都在 16.7 ms 内。

50 Hz 输出/输入的同一实验对照：输出变化画面间隔 P99 36.69 → 26.00 ms，
输入阶段变化画面间隔 P99 21.59 → 17.46 ms；排除 CLI 启动的输入请求往返
P99 17.97 → 10.20 ms。文件 `/tmp/water-cadence.paf_o8io` 与
`/tmp/water-cadence.mqnwnjs_`。这些短时间实验不能证明数小时内绝无卡顿。
最终字符尺寸通知变更的语言/grid 回归也通过，文件
`/tmp/water-language-grid.ugo9iduq`。

后续 1 MiB/s、20 秒、不强制 GC 测量：RSS 约 297–303 MiB，macOS
physical footprint 约 352–358 MiB，GPU image 64 MiB。后者高于此前测量，
显示同步的本地渲染/系统分配不计入 Go 堆软限制，仍不是 300 MB 硬上限。
结束时 replay 查询的临时分配仍存在。文件 `/tmp/water-cpu.nlshrrgl`。
隐藏期间输出并恢复通过，文件 `/tmp/water-background.hrqds291`；GUI 设置
回归文件 `/tmp/water-settings.g7wwgy46`。

`scripts/diagnose-gui-cadence.py --water DIAGNOSTIC_BINARY` 测量 50 Hz 输出
和控制输入，走 Water transport 与真实 UI handler，排除每次 CLI 启动成本；
不能替代物理键盘/IME 手动验证。`native.present` 表示提交变化画面的 Draw，
不是 WindowServer 实际扫描上屏的硬件计时。间隔包含输出源、系统调度、
显示器与绘制的影响，不能只拿平均 FPS 证明无卡顿。

以下保留此前阶段的测量用于对照。

本次修改降低字体和 GPU 常驻内存，移除输入路径上的终端解析锁与屏幕复制，
修复控制输入等待下一次维护帧的问题。验证使用 macOS 原生 GUI、真实 PTY 和
Water control API；没有替换或终止用户正在运行的安装版。

## 测量结果

统一使用 MiB（1 MiB = 1,048,576 字节）。RSS 与 macOS physical footprint
是不同口径，后者包含系统计入该进程的其他内存，不能只以 Go 堆代替。
所有最终内存测试均未强制 GC。使用现有用户配置的字体与窗口设置，测试实例
单独创建配置、socket、GUI 和内嵌测试 server，因此包含其额外开销。

| 场景 | RSS | Physical footprint | GPU image 内存 |
| --- | ---: | ---: | ---: |
| 修改后空闲 | 241 MiB | 263 MiB | 64 MiB |
| 0.25 MiB/s 连续输出，40 秒 | 286–304 MiB | 308–335 MiB | 64 MiB |
| 1 MiB/s 连续输出，25 秒 | 296–307 MiB | 331–339 MiB | 64 MiB |

40 秒实验后半段 RSS 从约 296 MiB 到 304 MiB，仍有小幅预热增长。
结果接近 300 MiB，尚未证明数小时输出完全不增长，也没有实现 300 MB 硬上限。
更大的窗口、更多分屏或大字号字体可以超过这些值。

内存脚本结束时反复通过 `terminal contains` 检查完成标记，会扫描服务器的
原始 replay，造成额外临时分配：40 秒实验结束样本 RSS 375 MiB、footprint
354 MiB；1 MiB/s 实验结束样本 RSS 380 MiB、footprint 355 MiB。
这不是持续渲染阶段的样本，不能隐藏，也不能用强制 GC 将其抹去。
本次没有改变该控制 API 的 replay 查询实现。

旧版本隔离输入测试中最长控制请求往返约 1,978 ms。修改后按用户配置以
4 MiB/s 持续输出 25 秒，每约 100 ms 注入一次真实 GUI 按键，结果为：

| 指标 | P99 | 最大值 |
| --- | ---: | ---: |
| 控制按键请求往返（243 次） | 90.44 ms | 94.89 ms |
| 原生 Update（1,530 次） | 1.60 ms | 8.00 ms |
| 原生 Draw（1,713 次） | 5.09 ms | 16.32 ms |
| 工作线程终端事件（5,282 次） | 0.37 ms | 5.07 ms |

控制请求包含 CLI 启动、传输和回复耗时，不能当成物理键盘延迟。
原生计时的最大值覆盖整个记录窗口，P99 使用最近至多 1,024 个样本。
该压测没有复现原来的秒级停顿；这不构成所有机器、负载下绝无停顿的保证。
系统键盘和 IME 的手动验收仍需要单独进行。

## 原因与修改

- Apple Color Emoji 的所有 bitmap strikes 一次加载约 182 MiB。
  现在按字号读取合适的 strike；放大字号仍可异步加载更高分辨率。
  原生字体只移除未使用的默认实例 `gvar` 轮廓变化数据，保留影响度量的表。
- typesetting 原先提前展开整套 CJK 字形轮廓。现在保留压缩轮廓和度量头，
  按需解析，最多缓存 1,024 个字形。配置的 Sarasa 来源可复用作 CJK fallback。
  该依赖补丁随源码固定，升级说明见 `third_party/typesetting/WATER_CHANGES.md`。
- 窗口 mask 与大面积 vector 路径占用额外 GPU atlas。
  圆角改为共享 shader，终端大画布使用独立纹理；GPU image 从 128 MiB 降至 64 MiB。
- 输入原先等待输出解析锁并立即复制终端屏幕。现在输入只提交发送命令与
  原子标记，工作线程处理回到底部和屏幕发布；PTY 顺序及 sequence 去重保留。
- Draw 可以先于消费排队输入的 Update 运行，旧调度器可能因此等到 250 ms
  维护唤醒。现在有排队输入时继续调度并立即唤醒原生循环。
- 固定低内存限制会在存活字体较大时造成 GC 抖动。GUI 使用至少 192 MiB
  的软预算，并为存活数据保留 64 MiB 余量；显式 `GOMEMLIMIT` 优先。
  server/CLI 不应用 GUI 预算。

## 验证与复现

已通过完整 `go test ./...`、`go vet ./...`、相关 GUI/终端/client/server race
测试、诊断构建测试，以及本地 typesetting fork 的单元和 race 测试。
字形测试逐一比较 Go 标准字体的轮廓与度量、并发缓存淘汰，原生字体测试覆盖
SFNS/Emoji 默认度量与不同字号。真实 GUI 设置和语言/grid smoke 通过，覆盖
终端按键、字体样式、中英文与 Emoji、分屏、拖动、窗口和配置保存。

复现命令与隔离说明见 `Documents.md` 的 Startup options。
此次本机完整实验文件：

- `/tmp/water-cpu.p70evmso`：40 秒内存复测。
- `/tmp/water-cpu.y5v5w655`：1 MiB/s 内存复测与渲染截图。
- `/tmp/water-input.m78g7crr`：4 MiB/s 输入与原生计时。
- `/tmp/water-input.mflwaq7d`：旧安装版隔离输入对照。
- `/tmp/water-settings.vm7gu4o8`、`/tmp/water-language-grid.dvff0l8t`：GUI 回归。

临时文件可能被系统清理。当前普通 dev 验证二进制位于
`target/go-ui-smoke/water`，不是已签名发布包；用户安装版仍运行原代码。
