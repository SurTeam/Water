# water-server CPU 调查与优化（2026-10-15）

本机复刻当前终端负载，独立 server 的 36 秒平均 CPU 从 **48.45% 降到 2.19%**，降幅 **95.47%**。优化后的七个完整 5 秒窗口均为 **2.00%–2.40%**，达到本次负载下低于 10% 的目标。CPU 按进程 CPU 时间差 / 墙钟时间差计算，一个 CPU 核为 100%。测量不包含负载发生器或 GUI 的 CPU。

## 现场与原因

用户运行的是 `/Applications/Water.app` 的 release 0.4.12：GUI PID 25710、server PID 26385，7 个 workspace、29 个终端、6 个 Agent、1 个 GUI session，Web listener 停止。15.04 秒现场计时中，server 为 **53.65% CPU**；PTY 输出 571,216 字节，约 **37.1 KiB/s**，流事件约 **94.4 次/秒**。该窗口没有模型 snapshot push。

原生 `sample` 的 Go 符号经过 `go tool addr2line` 还原后，包含 `publish → trimReplayLocked → shrinkPrimaryRedrawsLocked → compactRedrawSegments`。源码确认：只要含转义的历史总量大于单屏预算，每次追加输出都会遍历并拼接全部历史，再按换行切分检查，即使没有一个片段需要压缩。六个 Agent 的原始 replay 总计约 39.8 MB，小量持续输出会反复处理大量不变的历史。

修复在 `internal/goterminal/replay_history.go` 和 `registry.go`：追加输出时增量维护末尾片段长度及是否含 ESC；只有片段可能超限，或屏幕预算改变时，才运行原有完整压缩器。真正压缩后重建末尾片段的元数据。锁、压缩规则、历史预算、PTY 字节和有序流不变。

## 同输入对照

保存六个 Agent 的真实 `terminal.snapshot`，并通过现有 `terminal.attach` 录下同一 12 秒内的实际输出时序。隔离实例使用独立 socket/config，7 个 workspace、29 个终端，其中 6 个终端先注入保存的历史，再按录制时序输出；其余 23 个运行 zsh `-f`。消费者读取真实 attach 流。测量期间无 GUI 和 profiler；真实 GUI 另行验证。

录制一次含 1,173 个输出事件、500,603 字节。长窗口连续回放三次，总计 3,519 个录制事件、1,501,809 字节，另有六个完成标记，共 126 字节。两版 PTY 读取和发送计数均为 1,501,935 字节。PTY 合批后的事件数受调度影响略有差异，未以事件数相等冒充字节一致性。

| 窗口 | 修改前 | 修改后 |
| --- | ---: | ---: |
| 首轮约 12 秒 | 46.25% | 2.41% |
| 长窗口约 36 秒 | 48.45% | 2.19% |
| 长窗口内完整 5 秒区间 | 41.92%–52.99% | 2.00%–2.40% |

所有输入历史及 live 输出经过真实 PTY 和 attach。逐终端校验接收字节的 SHA-256；两版最终保留的六份 replay 的字节数和 SHA-256 也完全一致。两版 replay 保留字节变化均为 -319,931 字节。

基线是在本次编辑前构建的仓库 HEAD，修改版来自本次源码。每个测试的 `ownership.json` 和 `cleanup.json` 记录其 server PID、socket 和退出结果。所有四次成功测量均退出码 0、socket 已删除；未终止或替换用户 GUI/server。

## 复现与证据

复现工具为 `scripts/diagnose-server-cpu.py`。录制文件只保存在本机 `/tmp/water-server-cpu-evidence`，含终端内容，不纳入 Git。格式为 `agent-N.json` 的 snapshot 响应，以及 `live-N.json` 的 `{at, data}` 事件列表；`data` 为 base64，`at` 为相对秒数，N 为 0–5。

```sh
go build -o /tmp/water-server-cpu-evidence/optimized ./cmd/water-server
~/.venv/bin/python scripts/diagnose-server-cpu.py \
  --server /tmp/water-server-cpu-evidence/optimized \
  --recording /tmp/water-server-cpu-evidence --cycles 3
```

原始证据：

- [现场 CPU/metrics](/tmp/water-server-cpu-evidence/live-measurement.json)
- [还原符号后的原生 sample](/tmp/water-server-live.symbolicated.txt)
- [12 秒基线](/tmp/water-server-cpu.qtb759se/report.json)
- [12 秒修改版](/tmp/water-server-cpu.bmigwt6t/report.json)
- [36 秒基线](/tmp/water-server-cpu.yeaws2pk/report.json)
- [36 秒修改版](/tmp/water-server-cpu.2s4wo6ja/report.json)

## 验证与限制

通过：

- `gofmt`、`git diff --check`、`go vet ./...`。
- 针对 replay 的回归：随机分块、跨块转义序列、长普通行/转义重绘、备用屏幕切换、换行裁剪和 resize；新实现与强制每次完整扫描的原实现逐步比较 replay。
- 相关六个模块 `goclient/goserver/goterminal/govt/xterm/goui` 的 race 检查。
- 原生 macOS/Metal GUI 的 `go-ui-content-smoke.py`：400 行历史分页、真实滚动 viewport、读取无选区/滚动副作用、查询边界和截图；测试实例清理且用户实例保留。证据在 `/tmp/water-content.3v3uxune`。

初次优化验证的完整 unit profile **没有全部通过**。`vet` 通过，`go test ./...` 中 `goui` 进程曾在 server 处理 `terminal.send_bytes` 时崩溃：`commands.go:609` 对 registry 查询结果调用 `Terminal.Write` 时为空；其余包通过。随后带 JSON 输出单独诊断 `goui` 通过，相关 race 检查也通过，未确定第一次失败原因，不能据此宣称已修复或整个 profile 通过。失败证据：`target/test-runs/20261015T025510Z-fc102812`，诊断输出 `/tmp/water-server-cpu-evidence/goui-diagnosis.jsonl`。

## 0.4.13 发布验证

后续发布检查使用稳定的 `TMPDIR=/tmp`，完整 `terminal` profile 已通过，包含全量单测、vet、race、4 个 scenario 和真实 GUI 检查（PTY 查询、图像历史、Pi 重绘等）。证据：`target/test-runs/20261015T041505Z-b62b140d/summary.json`。此前空指针崩溃仍保留为历史失败记录，未宣称定位或修复其原因。

macOS arm64 release 0.4.13 已使用本机 SurTeam 证书签名，嵌套 updater/server/GUI 及 app 均通过验证，ZIP 解包后的签名再次验证通过；签名 release 本体也通过真实 GUI 历史分页和滚动检查，证据在 `/tmp/water-content.5ngnupvv`。用户安装版和会话未替换或重启。

这次对照复刻的是用户终端的历史大小与录制输出时序，未运行新的模型请求，也不保证任意吞吐或超长无换行重绘都低于 10%。
