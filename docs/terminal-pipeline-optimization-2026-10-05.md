# Terminal run and snapshot optimization — 2026-10-05

This change reduces CPU work in terminal run preparation and memory used by
visible snapshot cells. It does not replace the text engine or change output
publication frequency. Follow-up native GUI verification completed after the
display recovered. **The matched ANSI workload did not show a whole-GUI CPU or
physical-memory improvement**; the demonstrated benefits remain local.

## Current baseline

An isolated native macOS diagnostic GUI used a unique socket/config, detected
Homebrew zsh `-f`, a 96×30 grid, and the existing 1 MiB/s bounded ANSI generator.
Both measured states were visible and not occluded. Each CPU profile requested
four seconds; reported elapsed windows include profiling overhead.

| State | CPU | RSS | Physical footprint | Live Go heap |
| --- | ---: | ---: | ---: | ---: |
| Idle | 2.32% | 173 MiB | 162 MiB | 32 MiB |
| ANSI output | 20.17% | 247 MiB | 201 MiB | 71 MiB |

Actual output consumed during the measured window was 0.886 MiB/s. These figures
include the test's embedded server and are not directly comparable to the user's
installed GUI with a separate server. Baseline evidence is in
`/tmp/water-cpu.w7vdswpx/` (report, CPU/alloc profiles, native sample, vmmap and
screenshot). The binary is `/tmp/water-perf-baseline`.

Allocation-profile delta sampled 148.73 MiB: `buildGlyphs` 28.04 MiB, VT
`snapshot` 27.71 MiB, Harfbuzz shaping 17.13 MiB. Allocation profiles are
statistical; these are allocated bytes, not retained memory.

## Changes

- Empty terminal cells no longer generate font text unless underline,
  strikethrough or enabled hyperlink decoration needs a run. Their background
  still renders. Explicit spaces remain text, and empty gaps still separate
  runs at the correct columns.
- Run joining remembers whether the current run can be joined, avoiding rescans
  of its growing text for emoji/icon/block classification on every new cell.
  Classification of incoming cells and existing shaping/fallback remain intact.
- Text builders reserve space through the final cell needing text, rather than
  the whole row's empty padding.
- `govt.Cell` fields are reordered to remove alignment padding: 72 → 64 bytes on
  this arm64 host. A visible cell array uses 11.1% less space; this is **not** an
  11.1% reduction of the whole app's memory. Text strings, fonts, graphics,
  scrollback and other allocations are unchanged. Snapshot immutability and
  row sharing remain intact.

## Matched local benchmarks

`BenchmarkPrepareTerminalRow`, Apple M1 Pro, Go from `go.mod`, ligatures enabled,
500 ms per case, two repetitions per version. Rows come from the real emulator;
prepared-row slice capacity is reused as it is in the renderer. Times below are
the mean of the two samples; this is a microbenchmark, not a GUI CPU measurement.

| Row | Baseline | Modified | Time reduction | B/op baseline → modified |
| --- | ---: | ---: | ---: | ---: |
| 160 columns, 20 text cells | 15.09 µs | 8.05 µs | 46.6% | 160 → 24 |
| 160 columns, full text | 15.38 µs | 10.85 µs | 29.4% | 160 → 160 |
| 512 columns, full text | 53.28 µs | 35.20 µs | 33.9% | 512 → 512 |

All cases retain one allocation per operation. Blank rows now produce zero text
runs. Skipping those runs also avoids submitting their empty text for shaping,
but the total downstream shaping benefit has not been measured in the GUI.

## Reference designs and further work

[kitty's performance documentation](https://sw.kovidgoyal.net/kitty/performance/)
describes cached glyphs in GPU memory, separate I/O and rendering, and balancing
presentation delay against latency. Water already has worker parsing, bounded
snapshot publication, retained row textures and Ebitengine glyph-image caches.
Adding another atlas alone would not eliminate changing-row shaping allocations.

[tty7's terminal element](https://github.com/l0ng-ai/tty7/blob/main/src/terminal/element.rs)
uses `build_grid`, `segment_row`, `paint_glyphs` and `paint_run`. It reuses a flat
cell buffer, skips blanks without underline/strikeout, batches matching ASCII
styles and shapes text pieces through GPUI. Its Cargo manifest confirms GPUI and
an Alacritty VT core. This supports reducing empty-cell and run-building work;
it does not establish equivalent performance on Water's engine or workloads.

For a larger rewrite, the remaining evidence supports investigating compact
immutable cell/style frames and reusable shaped glyph geometry for changing
lines. Native font rasterization might reduce the large multilingual font heap,
but would require a separate cross-platform fallback, shaping and glyph-index
compatibility effort. None of those rewrites is implemented by this change.

## Follow-up native GUI measurements

After the display recovered, the final modified diagnostic build completed the
same four-second profile workload: output CPU 20.26%, actual consumption
0.884 MiB/s, RSS 242 MiB and footprint 197 MiB. The allocation-profile delta was
148.70 MiB, essentially unchanged from the earlier 148.73 MiB sample; sampled
snapshot allocation fell from 27.71 to 24.15 MiB, but other shaping/glyph samples
varied. Evidence: `/tmp/water-cpu.091ps7df/`.

A more useful matched comparison ran three bounded 1 MiB/s output cycles with
six-second CPU windows, diagnostic memory collection, **no CPU profiler and no
forced GC**. Both versions were visible and not occluded at all sampled boundaries.
They ran sequentially, so environmental and GC noise remain. Visibility was not
monitored throughout every window.

| Output cycle | Baseline CPU | Modified CPU | Baseline RSS | Modified RSS | Baseline footprint | Modified footprint |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 21.98% | 21.82% | 243.7 MiB | 245.5 MiB | 227.1 MiB | 227.3 MiB |
| 2 | 21.50% | 21.32% | 334.9 MiB | 340.0 MiB | 262.2 MiB | 279.1 MiB |
| 3 | 21.48% | 22.82% | 362.0 MiB | 349.8 MiB | 279.7 MiB | 289.5 MiB |

Mean output CPU was **21.65% baseline versus 21.99% modified**. There is no
measured whole-GUI CPU improvement. RSS and footprint do not show a consistent
reduction either. Without forced GC, sampled live heap varied between 59–68 MiB
on baseline and 66–89 MiB on modified; it does not establish a retained-memory
regression or a leak. The compact-cell result is a byte-layout guarantee, not
an observed percentage reduction of the process footprint.

Reports: `/tmp/water-cpu._g_ylcw3/report.json` (baseline) and
`/tmp/water-cpu.9kvqvnpb/report.json` (modified). Each includes a screenshot and
vmmap summaries. The GUI PID is recorded in each report and each script cleaned
up its own process.

## Verification and limits

Passed: `go test -timeout=55s ./...`, `go vet ./...`, race tests for `goui` and
`govt`, native dev build, blank/background/decoration tests, existing Unicode,
icon/ligature and randomized immutable-frame regressions. Added layout test
confirms the cell size reduction.

The initial modified GUI attempt failed before initialization with
`water: ui: no monitor was found at initializeGLFW` in
`/tmp/water-cpu.ep8hvu0p/gui.log`; the display was asleep then. Follow-up real GUI
tests used the final ordinary dev binary `/tmp/water-perf-optimized-dev`:

- `go-ui-wide-glyph-smoke.py` passed at **8, 16 and 32 pt**, including pixel checks
  for icon fitting, CJK/emoji, bold/italic, adjacency, wrapping, selection in both
  directions, block cursor and real zsh line editing. Artifacts and screenshots:
  `/tmp/water-wide-glyph.dj7yidua/`. The 16 pt glyph screenshot was also inspected.
- `go-ui-query-smoke.py` passed both phases: **36 actual PTY queries**, split-pane
  geometry, OSC7 directory and OSC52 clipboard copy. Artifacts:
  `/tmp/water-queries.un8sw9l2/`. The script restored the clipboard afterward.
- The modified query script's first run failed CSI 14t: UI grid expected
  `1408×1600` pixels while the reply reported `576×900`.
  `/tmp/water-queries.jqo9i_u5/failure-content.json` preserves the assertion.
  Subsequent investigation reproduced these exact replies deterministically:
  `resizeTerminal` returned before updating client cell metrics when the window
  had not yet received native focus. Layout exposed measured `16×44` cells, but
  the emulator still used the startup estimate `9×18`. Client pixel metrics now
  update before the focus guard; the guard still restricts shared PTY grid
  changes to the focused window. The regression test covers initial layout and
  later cell-metric changes without focus and asserts no PTY resize/dispatch.
- After that fix, `go-ui-query-smoke.py` passed three fresh GUI starts, each with
  36 real PTY queries plus OSC7/OSC52 (108 queries total). Evidence:
  `/tmp/water-queries.gcq5vmx8/`, `/tmp/water-queries.yhcraa12/`,
  `/tmp/water-queries.6j9zid8l/`.
- Shared-server multiwindow verification passed with focus-owned PTY dimensions
  changing between 162 and 80 columns and clean last-window server shutdown:
  `/tmp/water-multi-ziqr_9wx/`. Full Go tests, vet and `goui`/`govt` race checks
  passed again after the fix. The fixed dev executable is
  `/tmp/water-query-fix-dev`. The earlier CPU comparisons predate this query fix;
  no new whole-GUI performance benefit is claimed for it.

The follow-up verified the recorded query/wide-glyph GUI PIDs had exited.
Existing user GUI/server and `/Applications/Water.app` were left intact.

Built modified dev executable: `/tmp/water-perf-optimized-dev`. Reproduce the
measurements with diagnostic builds and:

```sh
go build -tags water_cpu_diagnostic -o /tmp/water-perf-optimized ./cmd/water
~/.venv/bin/python scripts/diagnose-gui-cpu.py \
  --water /tmp/water-perf-optimized --profile --seconds 4 --rate-mib 1
go test -timeout=55s ./internal/goui -run '^$' \
  -bench BenchmarkPrepareTerminalRow -benchmem -benchtime=500ms -count=2
```

Use matched longer non-profiled windows and multilingual/split-pane workloads
before claiming a fixed whole-app improvement. These local code and build changes
are not an installed or signed release.
