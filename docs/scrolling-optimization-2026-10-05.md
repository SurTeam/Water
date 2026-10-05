# 历史滚动优化与验证（2026-10-05）

本轮针对已有滚动调查定位的重复快照、原生菜单发布和无新画面的 Update。实现不新增限频、timer、ticker 或 sleep；每个滚轮事件仍完整处理并立即唤醒变更画面。

普通原生构建的小步滚动对照，从 **15.73% CPU 降到 9.61%（下降 38.9%）**。这是同一机器、相同 120 次滚动和相同位移的 8 秒窗口，不是长期均值或所有负载的保证。

## 实现

- `workspace.go`：拒绝的终端鼠标事件不再创建快照；接受的事件仍清除选择、刷新快照并 flush VT 回复。历史滚动、旧滚动入口和选择自动滚动使用 `FrameSnapshot()`，复用重叠可见行的不可变 cells。
- `workspace.go`、`ebiten_window.go`：滚轮和历史滚动通过现有 `invalidateOutput()` 立即增加画面 revision、唤醒画面，不再把离散滚动记录成需要持续活跃 120 ms 的输入。没有设置任何事件或绘制频率上限。按键、拖拽和 IME 的既有持续输入条件保持原有路径；选择拖拽仍有连续输入状态。
- `ebiten_platform_darwin.go`：菜单标题、快捷键和修饰键按配置变化失效；enabled、hidden、occluded、window number 和 layer 状态仍随每次发布检查。不变时复用已发布数据，改变时用 copy-on-write 构建新菜单和原生状态，保持旧快照不可变。
- 保留行内容 hash；本轮没有证据需要重写 row identity 或 hash 匹配。

## 对照条件与结果

Apple M1 Pro、在线内建显示器、原生 macOS GUI，96×30 终端、10,000 行历史预算、6,000 行编号 ANSI 历史；写入完成后 shell 停在无输出的 `read`。唯一 socket/config 和已记录 GUI PID；结束时仅清理本次实例。没有操作已安装的用户 GUI/server。

驱动通过 Water 自己的 `ctl ui wheel` 投递目标 15 个事件/秒。该输入速率只是比较负载，源码没有 15 Hz 限制。每个有效对照完成 120 次事件，全部 handled；小步和 Unicode 的 viewport 为 4975→4855，快速为 4975→3055。诊断样本的 PTY output bytes 均为 0，窗口可见且未遮挡。

诊断构建启用相同 CPU profiler、阶段计数和前后 alloc_space 样本：

| 负载 | CPU 优化前→后 | 降幅 | 窗口内采样分配量前→后 | Update 前→后 | 实际新行重绘前→后 |
|---|---|---|---|---|---|
| ASCII，1 行/事件 | 18.11%→10.00% | 44.8% | 86.32→33.39 MiB | 820→282 | 120→120 |
| ASCII，16 行/事件 | 19.25%→12.24% | 36.4% | 140.23→93.62 MiB | 795→287 | 1920→1920 |
| Unicode，1 行/事件 | 18.62%→12.60% | 32.3% | 87.45→41.78 MiB | 814→360 | 120→120 |

小步分配减少 61.3%，快速减少 33.2%，Unicode 减少 52.2%。小步 snapshot 直接采样分配由 25.15 MiB 降至不足 top-5 的范围；快速 snapshot 直接分配由 40.73 MiB 降至 16.10 MiB。快速的新行转换、glyph build 和 shaping 仍有实际成本。减少的是重复处理和无新画面的更新，不减少历史移动或新行重绘。

为检查诊断开销影响，额外运行**无诊断 build tags、无 CPU profiler、无阶段计数**的普通原生构建：

| 小步 ASCII | CPU | 事件数 | 位移 | CLI 单次平均耗时 |
|---|---|---|---|---|
| 优化前 | 15.73% | 120 | 120 行 | 52.23 ms |
| 优化后 | 9.61% | 120 | 120 行 | 45.78 ms |

普通基线通过 Go build overlay 恢复本轮前的三个实现文件，保留此前 Cell 布局、glyph pipeline 和像素回复修复，保证比较只针对本轮改动。overlay 和构建都位于隔离临时目录，没有回退工作区源码。

这些数值是窗口内 CPU 时间差/实际 wall time、pprof 采样累计分配差；不能把分配量下降写成 RSS/常驻内存同比下降。本轮没有证明 GPU image cache 或常驻历史内存下降。CLI 创建与 socket 往返占输入驱动耗时，不能据此声称验证了高频物理触控板；原生触控板、长期稳定性和所有 IME 的完整行为没有穷尽验证。

## 验证

- 新回归在旧实现上分别失败：拒绝鼠标重建 snapshot；历史滚动复制重叠行。修改后通过。
- 实际 attach callbacks：拒绝/接受鼠标、selection 清除和回复 flush；历史及选择自动滚动的位移、绝对选择坐标、复制内容、行共享、独立 Snapshot 内容一致以及旧帧不可变。
- 滚动立即增加 frame revision 和唤醒 channel，不触发持续 input activity；原生菜单动态 enabled 每次读取，配置变化立即重读静态属性，旧发布 map 保持不可变，稳定菜单 snapshot 为 0 allocations。
- `go test -timeout=55s ./...`、`go vet ./...`、`go test -race -timeout=55s ./internal/goui ./internal/govt`、原生普通/诊断 GUI 和 dev server 构建通过。
- 真实 GUI：生命周期菜单/快捷键、隐藏/显示、最小化/恢复、多窗口及独立 server 生命周期；Settings 现场修改 hide shortcut 后原生菜单立即变更、实际新快捷键生效；圆角和窗口几何；8/16/32 pt CJK/emoji/icon 双向完整字符选择、样式、邻接、换行、cursor 与 zsh 输入。
- 初次修改版生命周期测试在隐藏后 Show Water 恢复发生一次超时。旧诊断基线通过；**没有源码变更的同一修改版**随后通过完整生命周期，之后 Settings 现场修改后的 hide/show 也通过。首次超时原因未确定，不能声称已经修复或证明与本轮改动无关；保留失败及复查目录。

## 证据

诊断驱动：`/tmp/water-scroll-profile.py`。普通构建驱动：`/tmp/water-scroll-normal.py --no-profile`。

| 负载 | 基线目录 | 修改版目录 |
|---|---|---|
| 小步 | `/tmp/water-scroll.8olbn765` | `/tmp/water-scroll.6mbmzz8r` |
| 快速 | `/tmp/water-scroll.aa_bbyqh` | `/tmp/water-scroll.8zfcgnhs` |
| Unicode | `/tmp/water-scroll.x_5uq_qf` | `/tmp/water-scroll.0b24kxsp` |
| 普通构建小步 | `/tmp/water-scroll.nebev3v5` | `/tmp/water-scroll.5q986ehz` |

每个诊断目录包括 report.json、CPU/前后 allocs profile 和截图。普通目录包括 report.json 和截图；没有 profiler/分配数据。

构建及基线 overlay：`/tmp/water-scroll-build.fxthkH`。

原生生命周期首次超时：`/tmp/water-lifetime.xuxd68ye`；旧基线通过：`/tmp/water-lifetime.9a2jxk8j`；相同修改版后续通过：`/tmp/water-lifetime.svo1_lh7`。Settings 与菜单缓存现场验证：`/tmp/water-parity.hn5lq_r1`。宽字符与选择：`/tmp/water-wide-glyph.tqjp2g3s`。

实现和报告留在工作区；本轮没有提交、安装或发布产物。
