# 测试与验证

This is the execution guide referenced by `AGENTS.md` and the bundled
water-control skill. Use the scripts as the shared procedure so verification
does not depend on which model is performing the work.

## Choose the smallest useful check, then the delivery profile

First inspect `git status` and the affected implementation. Identify the exact
failure and its observable result. Run a targeted regression while fixing it.
Before delivery, run the matching profile once against the final code:

```sh
~/.venv/bin/python scripts/run-go-regression.py --profile full --list
~/.venv/bin/python scripts/run-go-regression.py --profile full
```

| Profile | Checks |
| --- | --- |
| `unit` | Python test-harness fault checks, full Go tests, vet, race checks for transport/PTY/VT/UI |
| `terminal` | Unit profile, versioned dev GUI/server build, all scenarios, native GUI smoke, PTY queries, repeated CJK/large clipboard paste, generated-image/history checks, real Pi redraw |
| `agent` | Unit profile, builds, all scenarios, native GUI smoke, status-icon pixel measurements, installed Agent startup/sidebar switching, real Pi redraw |
| `full` | Union of terminal and Agent checks; each step runs once |

`go-ui-output-isolation-smoke.py` is included in every GUI profile. It parses a 500,000-line ANSI/CJK burst, then runs roughly 4 MiB/s continuous output while checking another pane’s input/output, new workspace creation and shortcut closure of the flooding pane. It verifies child-process exit and active PTY removal, and records control round-trip times without asserting a fixed latency target. It does not establish CPU/RSS bounds, long-term leak freedom or physical-keyboard behavior.

GUI/clipboard test lifetimes use the shared per-user advisory lock in `scripts/water_test.py`, including directly invoked legacy scripts and shell runners. Acquire it before process baselines and desktop/clipboard access; release it after cleanup and clipboard restoration. The regression runner executes steps sequentially without a second outer lock. A competing test’s lock wait currently counts toward the step deadline.

Cleanup verifies owned GUI/server exit, socket removal and preservation of pre-existing Water processes. Ownership failures refuse shutdown; ordinary transport or evidence failures still clean up directly owned processes. Cleanup errors do not replace the original test failure. Clipboard backups are deleted after successful restoration and excluded from evidence exports, including failure paths.

Regression launchers must preserve the default SIGHUP disposition. `nohup` passes
ignored SIGHUP into test shells and invalidates process-group close tests. For
background runs use a new session with SIGHUP restored to its default. Process
termination checks distinguish a running descendant from an exited zombie still
awaiting OS reaping, and report process state on failure.

PTY close regression checks must exercise ignored HUP/TERM, separate job process
groups, traceable descendants that create another session, and neighboring PTY
isolation. Cover both in-process and supervisor close paths, bounded enumeration
failure, and reaped/replaced root identities. A historical PID must never grant
permission to discover or kill an unrelated session. These checks do not prove
cleanup of already-reparented descendants outside the PTY session.

Each step has a 60-second deadline. The whole profile consists of multiple
steps and can take longer than a minute. A failure stops the profile; do not
silently skip a dependency or repeat the same failed test until it passes.
Resolve the smallest confirmed cause, rerun that check, then run only checks
whose result could be affected by the new change.

The runner uses the active Go version from `go.mod` and the Python interpreter
used to invoke it. GUI profiles require native desktop access, zsh, Pillow,
`kitten`, and the installed Agent CLIs exercised by the scripts. Pi discovery
loads the fnm environment. A generated PNG supplies the image fixture so the
suite does not depend on a particular file in a user's Pictures directory.

## Match the evidence source to the assertion

| Assertion | Observation |
| --- | --- |
| Model topology, Agent classification, command completion | `state`, operation/revision and the real dispatcher |
| Ordinary shell input/output | `pane content` or bounded terminal output conditions |
| GUI viewport, retained history, Agent clear/redraw policy | `ui content`, with the owning connection socket and explicit pane/window UUID |
| Actual pixels, icon dimensions, layout and clipping | `ui screenshot`, optionally compare a bounded image crop |
| Input behavior | `ui key/click/wheel` through Water control, then assert resulting output/model/viewport |
| Selection, copy and paste | Real selection/clipboard path, preserve and restore native clipboard types |

`pane content` creates a separate emulator from server replay. Its result is
not the GUI's current viewport or historical buffer. `terminal snapshot`
contains raw replay events. Never use either as proof that GUI scrollback or
client-specific clear handling is correct.

`ui content --pane UUID [--window UUID]` reads the current client viewport.
`--start-row N --rows M` reads physical rows from the retained buffer without
moving the viewport. Requests are bounded to 256 rows and 1 MiB of cell text.
The response identifies the window, pane and terminal and reports `last_seq`,
`total_rows`, `y_base`, `y_disp`, `trimmed_lines`, `alt_screen`, and
`synchronized_output`. This is the client's latest parsed state, not proof that
Draw has rendered it; capture a screenshot for rendering assertions.

For paginated history, all pages must match sequence, trim count, active buffer
and total row count. Restart a changing range; never stitch inconsistent pages.
Wait for a concrete fixture marker or state transition and completed synchronized
output. Do not use a fixed delay or an arbitrary quiet period as completion.

Text checks must not drag-select, copy, use `pbpaste`, or OCR. Those paths alter
selection, viewport hold or the clipboard and belong only in tests of those
features. Ordinary content tests must assert that selection and scroll position
are unchanged by their queries.

## Reuse the owned GUI fixture

New Python GUI checks should use `scripts/water_test.py`:

```python
from water_test import WaterGUI

with WaterGUI("water-example.") as test:
    pane = test.ctl("ui", "snapshot")["frame_focused_pane"]
    before = test.content(pane)["last_seq"]
    test.ctl("ui", "key", "text:echo WATER_EXAMPLE_OK")
    test.ctl("ui", "key", "enter")
    test.wait(
        lambda: (lambda c: c["last_seq"] > before
                 and "WATER_EXAMPLE_OK" in c["text"])(test.content(pane)),
        "input/output round trip",
    )
    test.save("content.json", test.content(pane))
    test.screenshot("result.png")
```

The fixture creates a unique `/tmp` directory, socket and config, starts an
owned GUI with isolated zsh startup, records GUI/server/window ownership, and
cleans up only that instance. Startup polling can tolerate transient connection
failures. Once ready, unexpected control errors fail immediately. Waits have
explicit deadlines and poll observable conditions; `time.sleep(.025)` is only
the polling interval, not the completion criterion.

For Agent rendering, use a local saved session/tool-output fixture and the
installed renderer. Exercise real shortcuts without submitting a model request
or installing hooks. `go-ui-pi-redraw-smoke.py` demonstrates two Ctrl+O cycles
across a viewport, retained pre-Agent history, a single welcome, and keyboard
input/Enter following the bottom. No mouse selection or clipboard access is
needed. Screenshots should show the normal unselected terminal.

### Unfocused test windows

All `scripts/go-ui-*.py` tests, the shared `WaterGUI` fixture, the shell UI
smoke runner, and `run-go-regression.py` set `WATER_TEST_UNFOCUSED=1` before
launching a GUI. Water passes Ebitengine's `RunGameOptions.InitUnfocused`, so
the window is shown without activating the app or taking the user's key window
on macOS. The control API drives input (key, click, menu, content) while the
window remains unfocused.

This is separate from `WATER_TEST_INSTANCE`, which names the test window and
is used for ownership checks. Every GUI smoke script sets the unfocused flag
itself, so running one directly has the same startup behavior as running it
through the regression runner. `window_focused` remains the actual OS focus
state; startup checks should wait for a frame/menu/pane rather than requiring
focus. Tests specifically covering focus transitions should assert visibility
and unfocused status, while the background smoke may activate its configured
external app to exercise occlusion.

### Clipboard text paste regression

`go-ui-text-paste-smoke.py` belongs to `terminal` and `full`. It preserves and
restores all native clipboard types and sends actual `ui key cmd-v` actions.
It verifies 80 repetitions of `啊啊啊啊啊啊啊` and a single 256 KiB UTF-8
payload with bracketed paste both disabled and enabled. Raw-mode PTY receivers
read in a loop and report exact wire length and SHA-256, including each bracketed
paste delimiter. A separately ordered end marker detects extra trailing bytes.
Echoed bytes exercise the GUI parser and drawing; screenshots
and GUI content are retained. Three additional ZLE command-line cycles verify
450 pastes using a distinct payload/digest per cycle.

Clipboard assertions must exercise the native paste path: text: input bypasses clipboard inspection. Native autorelease pools must stay on their creating OS thread for their whole lifetime; Go race checks cannot prove this. TestMacAutoreleasePoolStaysOnCreatingThread explicitly yields while native objects remain live.

Control-injected repeated shortcuts exercise the shared paste handler; they do
not simulate holding a physical key. Physical Cmd+V repeat/IME acceptance remains
a separate manual check. The large-paste assertion covers the stated 256 KiB
payload, not an unbounded clipboard size.

### Native rendering and restore checks

macOS GUI profiles run `go-ui-metal-readback-smoke.py`: its native fixture forces repeated readback flushes across autorelease pools, then tests both a subsequent draw and an empty-command-buffer present. Go race and cross-builds cannot verify native drawable lifetime. Ebitengine fixes are maintained in `third_party/ebiten/WATER_CHANGES.md`.

`go-ui-background-smoke.py` verifies screenshot readback while hidden/minimized and terminal round trips after restore. Ordinary measured requests retain a 500ms budget. Only the first snapshot following deminiaturization has a recorded 1500ms budget for the native AppKit/Dock transition; its actual latency remains in `latency.json`. A pass does not mean the native transition completed within 500ms. A native AppKit control reproduced event-loop blocking without Water/Metal; do not attribute the entire transition to terminal parsing or treat increased polling as a fix.

## Keep machine-readable evidence

The runner writes `target/test-runs/<run>/summary.json` with every command, PID,
duration, exit code, timeout and log path. Step logs contain each GUI test's
artifact directory. The shared GUI fixture writes `ownership.json`, failure
information if applicable, screenshots/content requested by the test and
`cleanup.json` with process/socket cleanup results.

The final report should state the requested behavior, passed checks and any
unverified boundary. Do not equate a cross-build with native execution or
control-injected input with physical keyboard/IME acceptance. After matching
checks pass, stop; unrelated cleanup and repeated suites do not add evidence.

## 原始输出吞吐与解析基准

大文件 fixture 不随仓库提交，通过 `WATER_CAT_BENCHMARK_FILE` 显式指定；未设置时相关测试跳过：

```sh
WATER_CAT_BENCHMARK_FILE=/path/to/benchmark.data \
  go test ./internal/gosupervisor -run '^TestCatBenchmark$' -count=3 -v
WATER_CAT_BENCHMARK_FILE=/path/to/benchmark.data \
  go test ./internal/goterminal -run '^$' -bench BenchmarkReplayIngestFixture -benchmem
WATER_CAT_BENCHMARK_FILE=/path/to/benchmark.data \
  go test ./internal/xterm -run '^$' -bench BenchmarkANSIFile -benchmem
```

supervisor 测试默认关闭 OPOST，对完整文件逐字节 SHA-256 校验；设置 `WATER_CAT_BENCHMARK_OPOST=1` 测量默认终端输出转换，此模式移除 CR 后校验其余字节及顺序，不验证 CR 的精确数量。日志的 `original_time` 是 shell `time cat` 结果，`elapsed` 包含测试端消费完成；两者都不是 GUI 呈现完成。完整链路验收必须重建 GUI 及其实际启动的相邻 server/supervisor 二进制，使用相同负载重复计时并另等 GUI 完成标记，同时验证持续输出和洪泛时交互。局部解析基准不能代替整链路、CPU 或内存压力测试。

交互回显延迟可用 `TestEchoLatencyAfterInput` 单独观察（真实 PTY 与 supervisor，测写 1 字节到首个输出事件），它不需要显示器，但不覆盖 GUI 发布、绘制与呈现：

```sh
 go test ./internal/gosupervisor -run '^TestEchoLatencyAfterInput$' -count=3 -v
```

GUI 侧分段计时（`input.send`、`input.ime`、`input.echo_latency`、`publish.present`、`input.present`、`present.miss`）只在 `water_cpu_diagnostic` 构建中存在，用隔离实例与 `scripts/diagnose-gui-input.py --timings` 采集；`input.present` 是从输入到包含该回显的帧被绘制的端到端值。

## 文档与交付时机

开发中的候选方案、调查与临时结果保留在会话上下文。用户确认代码实现并明确要求 commit 后，才依据最终代码和真实验证更新模块文档与必要的发布说明；用户单独要求修改文档时按该任务范围执行。测试通过不自动授权 commit、升版本、push、tag 或发布。完整流程见 [AGENTS.md](../AGENTS.md#文档更新时机)，签名与变体规则见 [构建与发布](build-and-release.md)。

## 多窗口验证

多窗口检查必须覆盖 detached/embedded 两种 server 生命周期、独立选择、焦点 resize 和仅清理自有实例。

```sh
~/.venv/bin/python scripts/go-ui-multiwindow-smoke.py --binary target/go-ui-smoke/water
~/.venv/bin/python scripts/go-ui-multiwindow-smoke.py --binary target/go-ui-smoke/water --embedded
```

## 现有 fixture 的剪贴板边界

`scripts/go-ui-query-smoke.py` 与文本粘贴测试使用 Swift fixture 备份并恢复全部原生类型；OSC52 断言仍读取文本以验证实际复制。备份在成功恢复后删除，导出证据排除备份；恢复失败保留本地恢复材料并报告失败，不能声称剪贴板已完整恢复。
