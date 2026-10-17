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
| `unit` | Full Go tests, vet, race checks for transport/PTY/VT/UI |
| `terminal` | Unit profile, versioned dev GUI/server build, all scenarios, native GUI smoke, PTY queries, repeated CJK/large clipboard paste, generated-image/history checks, real Pi redraw |
| `agent` | Unit profile, builds, all scenarios, native GUI smoke, status-icon pixel measurements, installed Agent startup/sidebar switching, real Pi redraw |
| `full` | Union of terminal and Agent checks; each step runs once |

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

## 文档与交付时机

开发中的候选方案、调查与临时结果保留在会话上下文。用户确认代码实现并明确要求 commit 后，才依据最终代码和真实验证更新模块文档与必要的发布说明；用户单独要求修改文档时按该任务范围执行。测试通过不自动授权 commit、升版本、push、tag 或发布。完整流程见 [AGENTS.md](../AGENTS.md#文档更新时机)，签名与变体规则见 [构建与发布](build-and-release.md)。

## 多窗口验证

多窗口检查必须覆盖 detached/embedded 两种 server 生命周期、独立选择、焦点 resize 和仅清理自有实例。

```sh
~/.venv/bin/python scripts/go-ui-multiwindow-smoke.py --binary target/go-ui-smoke/water
~/.venv/bin/python scripts/go-ui-multiwindow-smoke.py --binary target/go-ui-smoke/water --embedded
```

## 现有 fixture 的剪贴板边界

完整类型保全是测试原则，不代表所有旧脚本已经满足。`scripts/go-ui-query-smoke.py` 目前用 pbpaste/pbcopy 仅备份与恢复文本，会丢失原有图片/文件等类型；在需要完整保留用户剪贴板的环境不能把它当作无副作用验证。新增或修订测试应复用完整原生类型备份方案，不能只恢复文本后报告剪贴板已完整恢复。
