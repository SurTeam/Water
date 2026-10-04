# Water CPU 分析与修复（2026-10-04）

正常构建在同机、同配置下实测：空闲 CPU **42.36% → 5.12%**，1 MiB/s ANSI 持续输出 **52.22% → 32.98%**。这是进程 CPU 时间 / 墙钟时间，一个 CPU 核为 100%；每个窗口测量 8 秒。修复位于源码和 `target/cpu-after/water-dev`，没有替换 `/Applications/Water.app`。

## 采样找到的瓶颈

Water 的像素绘制使用原生 Metal GPU。高 CPU 的主要来源是 GPU 提交之前及提交过程中制造的工作：无变化时仍持续更新、重绘；每行独立的渲染目标与逐行交替合成；以及大量短命的原生调用参数、文字布局和快照对象。

原始基线为仓库 HEAD `48bd0c2`，在独立 worktree 原生构建。临时诊断构建同时采集 Go CPU profile、前后分配 profile 和 macOS `sample`；普通构建不包含诊断 HTTP 服务。原生 sample 的阻塞线程采样次数不能直接当作 CPU 占比。

1 MiB/s 输出的首轮 8 秒 CPU profile：

| 调用链 | 累计采样 CPU | 比例 |
| --- | ---: | ---: |
| Metal `Graphics.draw` | 690 ms | 18.75% |
| 窗口 `Draw` | 660 ms | 17.93% |
| `runtime.mallocgc` | 950 ms | 25.82% |
| 终端事件处理（含快照） | 100 ms | 2.72% |
| `Emulator.Snapshot` | 70 ms | 1.90% |

累计调用链存在重叠，不能相加。快照不是该负载的主导 CPU 热点；之前基于代码直觉把它作为主要瓶颈不成立。

随后用 10 秒窗口做分配差值。采集端在计时窗口之外完成两个 GC 周期，使分配记录追上当前状态，避免启动阶段字体加载混入差值：

| 原始基线 | 分配总量 | 主要来源 |
| --- | ---: | --- |
| 空闲 | 248.88 MB / 10 秒 | ObjC 调用链 131.01 MB；反射对象和 font face 等 |
| 1 MiB/s ANSI 输出 | 899.66 MB / 10 秒 | ObjC 调用链 233.01 MB；glyph 构建 161.25 MB；快照 136.03 MB |

核心绘制结构原先是「更新第 1 行纹理 → 合成到窗口 → 更新第 2 行纹理 → 合成到窗口……」。Metal 后端遇到不同 destination 会结束并重新建立 render encoder。文字 run、裁剪和颜色变化也需要提交原生绘制状态。GPU 执行绘制不能消除这些 CPU 工作。

## 修复

- 空闲窗口改为事件唤醒。模型/终端 worker 通过原子 revision 通知窗口；60 Hz 调度合并输出变化，按键重复、拖拽与 IME 时继续高频更新。周期维护与光标闪烁单独唤醒，无变化的帧不提交绘制命令。
- 终端行改用同一个 GPU 缓存面的不同 slot。先更新全部变化的 slot，再统一合成到窗口。滚动时按内容 hash 重新映射 slot，避免为滚过来的相同行重绘或创建新纹理。尺寸/字体/主题变化时整体更新缓存。
- PTY 事件仍在客户端 worker 按 sequence 解析，查询回复即时处理；有界队列内事件合并，可见快照最多按 60 Hz 发布。Output、Resize、Exit 顺序与去重不变。该项主要降低分配频率，不将其宣称为主导 CPU 修复。
- Settings 异步保存完成后通知重绘，隐藏恢复和截图可强制渲染。

中间的统一缓存面采样（10 秒）：Metal 绘制累计约 90 ms，相比基线 8 秒中的 690 ms，按单位时间下降约 90%；ObjC 调用链分配约 59.50 MB / 10 秒，相比 233.01 MB 下降约 74%。所有分配约 550.16 MB / 10 秒，相比 899.66 MB 下降约 39%。

剩余 CPU 的最大单项采样是 `runtime.madvise`，调用链来自堆内存页管理；文字 shaping/glyph 构建、不可变屏幕快照仍会产生分配。当前修复没有消除所有高吞吐输出成本，也没有测量 GPU 利用率或宣称 GPU 饱和。

## 复测与原始证据

最终正常构建不启用 pprof，不运行 native sample，也不强制 GC：

| 状态 | 修改前 | 修改后 | 降幅 |
| --- | ---: | ---: | ---: |
| 空闲 | 42.36% | 5.12% | 约 88% |
| 1 MiB/s ANSI 输出 | 52.22% | 32.98% | 约 37% |

原始文件：

- [正常构建修改前报告](/tmp/water-cpu.boapju6s/report.json)
- [正常构建修改后报告](/tmp/water-cpu.suh6ulx5/report.json)
- [基线空闲 CPU profile](/tmp/water-cpu.b2bee473/idle.pprof)
- [基线输出 CPU profile](/tmp/water-cpu.b2bee473/ansi-output.pprof)
- [统一缓存面输出 CPU profile](/tmp/water-cpu.gr27ut__/ansi-output.pprof)
- [最终采样报告](/tmp/water-cpu.g1b15cfw/report.json)

profile 目录还包含 allocation before/after、原生线程 sample、GUI 日志与截图。临时目录由本次测量创建；原始文件未提交 Git。

复现普通测量：

```sh
go build -o target/cpu-after/water-dev ./cmd/water
~/.venv/bin/python scripts/diagnose-gui-cpu.py \
  --water target/cpu-after/water-dev --seconds 8 --rate-mib 1
```

复现采样与分析：

```sh
go build -tags water_cpu_diagnostic -o target/cpu-profile-after/water-dev ./cmd/water
~/.venv/bin/python scripts/diagnose-gui-cpu.py \
  --water target/cpu-profile-after/water-dev --profile --seconds 10 --rate-mib 1
go tool pprof -top -cum target/cpu-profile-after/water-dev /tmp/<报告目录>/ansi-output.pprof
go tool pprof -top -sample_index=alloc_space \
  -base=/tmp/<报告目录>/ansi-output-allocs-before.pprof \
  target/cpu-profile-after/water-dev /tmp/<报告目录>/ansi-output-allocs-after.pprof
```

脚本使用独立 control socket、临时配置、检测到的 zsh `-f`，通过 Water control API 交互；正常测量使用嵌入式 server，只清理本次启动的 GUI PID。

## 验证与限制

通过 `go test ./...`、`go vet ./...`、关键客户端/服务端/GUI 模块 race 检查，以及真实 GUI 的布局/设置 parity、36 次 PTY 查询、OSC7/OSC52、Unicode 1 MiB/s 持续输出下两次隐藏恢复及终端往返检查。新增测试验证了滚动行匹配、重复 hash slot 不重复消费，以及有界事件批处理的顺序、尺寸变化和 sequence 去重。

宽字符完整像素 smoke 在 8pt 字体的同一断言失败：`(8, 8, 0, (0, 4, 16, 19))`。未修改的基线与修改后均失败，因此没有将该检查报告为通过，也没有扩大本次任务去修复它。两组失败证据分别在 `/tmp/water-wide-glyph.xybxaspl` 和 `/tmp/water-wide-glyph.ir2wmz02`。

结果是本机有限测量窗口的对照，不能直接预测其它字体、窗口大小、多 pane 或远端吞吐下的绝对 CPU。已安装用户实例未终止、未替换；仓库原有的四个未跟踪 server payload 未改动。
