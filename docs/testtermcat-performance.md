# testTermCat performance investigation — 2026-10-03

The core optimization is implemented while retaining Gio. In the native macOS
GUI, the unchanged `testTermCat` script now reports `real` values of
**0.753 / 0.685 / 0.653 seconds** (median **0.685 seconds**). The user's prior
interactive Go result was approximately 1.9 seconds; this comparison reduces
the reported producer time by approximately 64%. The isolated GUI configuration
and the user's original window may differ, so it is not a matched GUI A/B test.
Matched headless before/after measurements independently confirm the gain.

No Ebitengine implementation was benchmarked or added. The initial investigation
and profiles below explain why the terminal-processing and PTY reader paths
were optimized first.

## Implemented optimization and final verification

| Matched path | Before median | After median |
| --- | --- | --- |
| PTY → emulator, no GUI | 1.879 s | **0.792 s** |
| PTY → server/socket/client → emulator, no GUI | 1.933 s | **0.799 s** |
| Memory-fed emulator, 16 KiB chunks, profiled | 1.327 s | **0.710 s** |

Final unprofiled headless samples were 0.792 / 0.794 / 0.782 seconds direct,
and 0.835 / 0.783 / 0.799 seconds server-local. This is a roughly 2.4× speedup;
individual server-local samples can still exceed 0.8 seconds.

- Ordinary single underline uses the existing packed foreground flag instead
  of per-cell extended-attribute maps. SGR reset releases empty visual state
  while retaining OSC 8 identity and copy-on-write isolation for richer styles.
- Cell-size-query filtering passes ordinary ANSI data through without copying,
  retains split prefixes, and allocates only for actual filtering/carry work.
- OSC 8 scanning skips chunks that cannot introduce OSC while preserving a
  trailing ESC. Graphics scanning avoids copying chunks without graphics and
  checks UTF-8 boundaries only for actual C1 introducers. Packed erase ranges
  perform three stores per cell and remove sparse metadata once per range.
- Darwin's blocking PTY file is rewrapped as a nonblocking, pollable file with
  a separate owned descriptor. The reader accumulates available data before
  channel handoff and uses the Rust reader's bounded successful-data micro-burst:
  at most 64 consecutive EAGAIN attempts, a 1 ms idle window, a 5 ms maximum
  batch age, and a 128 KiB block. Empty readiness returns to the runtime poller;
  it does not enter the successful-data spin loop. These bounds permit brief
  active polling while data is arriving, trading CPU work for fewer handoffs.
- A raw-reader pause/flush barrier includes bytes held inside a micro-burst
  before publishing Resize. It resumes only after the geometry event, keeping
  old Output → Resize → new Output ordering. Replay and event queues retain
  their existing bounds; output is not discarded.
- `ui.snapshot.active_terminal_last_seq` reports the active client-owned
  terminal's consumed sequence. The GUI benchmark waits for the sequence of
  each final producer marker before taking the screenshot. The `real` values
  still measure producer completion, not final-frame GPU presentation latency.

Per-run allocations fell from approximately 1.615M to 22.6k objects direct,
and from 1.623M to 27–28k objects server-local. Memory-fed processing fell from
approximately 187 MB to **27 MB** allocated per run. Direct/server retained
output and transport allocations remain, so their total allocated bytes are
higher (~132–140 MB direct and ~197–202 MB server-local in the final run).

Validation passed:

- Full uncached Go suite, `go vet`, and client/server/PTY/VT/xterm/UI race checks.
- Cross-chunk query and OSC 8 regressions, zero-allocation ordinary ANSI and
  underline checks, rich-style isolation, sparse-metadata erasure, pollability
  after resize, and raw-reader pause/resume/EOF checks. The two reader/barrier
  checks also passed 20 repetitions under the race detector.
- All four existing scenarios and native macOS GUI click/input/screenshot smoke.
- 32 MB completeness/sequence/queue/replay performance gate: 96.2 MB/s direct,
  92.3 MB/s server-local. These are a different, plain-text workload.
- Interaction under flood: resize **1.40 ms**, Ctrl-C-to-exit **0.53 ms**.
- Linux amd64 core test cross-compilation. Native Linux runtime/performance
  verification was not available in this macOS session.

GUI artifacts: `/tmp/water-cat-perf.wett1aw0/`; the screenshot includes the final
`real 0m0.653s` output and completion marker. Test GUI/socket cleanup completed.
Built development executables are `target/go-ui-smoke/water` and
`target/go-ui-smoke/water-server`; installed user applications were not replaced.

## Workload and measurement boundaries

`/Users/clearain/.local/bin/testTermCat` executes:

```sh
time cat /Users/clearain/tmp/computer_use/benchmark.data
```

The file contains 61,400,000 bytes, 1,100,000 newline bytes, 6,800,000 ESC bytes,
and no non-ASCII bytes. It is an ANSI-heavy workload, including bold, inverse,
underline, attribute resets, carriage return, and erase-line sequences.
Existing plain-text benchmark results are not representative of this corpus.

The shell's `real` measures producer completion under PTY backpressure. It
does not by itself prove that the client parsed and displayed the last byte.
The headless benchmarks below instead consume through the ordered Exit event
and take a final terminal snapshot. They are useful for isolating bottlenecks,
but are not interchangeable with the shell's timer. The user's Rust number
was not independently reproduced during this investigation.

## Initial native Darwin arm64 measurements

Go 1.27.1, existing benchmark terminal size 80×24, scrollback 10,000. GUI window
size, terminal geometry, and scrollback in the user's interactive comparison
may differ. These initial samples precede the production optimizations above.

The existing benchmark was run with the command override pointing to the exact
file. Although its name still says 64MB, this run uses the actual corpus.
Use elapsed time and the custom `MB/s` metric, not the benchmark framework's
fixed `SetBytes(64_000_000)` throughput calculation. PTY newline expansion
produces approximately 62.5 MB of decoded output.

| Path | Three unprofiled samples | Median | Allocation per run |
| --- | --- | --- | --- |
| PTY → Go emulator, no GUI/server transport | 1.911 / 1.845 / 1.879 s | 1.879 s | ~258 MB, ~1.615M objects |
| PTY → Go server/socket/client → emulator, no GUI | 1.890 / 1.933 / 1.937 s | 1.933 s | ~328 MB, ~1.623M objects |

A separate memory-fed emulator probe replaces LF with CRLF to approximate the
same PTY output. It takes only a final snapshot and excludes PTY, server,
socket, GUI layout, and GPU drawing. These samples have CPU profiling enabled:

| Input chunk | Three samples | Median | Allocation per run |
| --- | --- | --- | --- |
| 16 KiB | 1.336 / 1.327 / 1.327 s | 1.327 s | ~187 MB, ~1.619M objects |
| 64 KiB | 1.314 / 1.316 / 1.330 s | 1.316 s | ~188 MB, ~1.613M objects |

Larger emulator input chunks alone barely improve this case. Direct and server
medians are close, so transport is not the first optimization target. These
pipelines overlap work; subtracting medians does not give an exact additive
cost breakdown.

## Concrete optimization targets

1. **Extended attributes and cell storage.** The sampled allocation-object
   profile attributes approximately 92.8% of objects to
   `xterm.(*ExtendedAttrs).Clone`. Investigate value storage, immutable/shared
   attributes, or copy-on-write. Preserve independent cell styles, underline
   colors/styles, and OSC 8 identities. The percentage is an allocation share,
   not a wall-time saving estimate.
2. **Avoid copying ordinary ANSI data for rare queries.**
   `Emulator.filterCellSizeQueryLocked` currently creates input and output
   buffers whenever ESC appears, even if the actual cell-size query does not.
   It accounts for approximately 42.9% of sampled allocated bytes in the
   combined direct/server profile. A no-query path can preserve the input
   slice, while retaining state for queries split across chunks.
3. **Consolidate auxiliary parsing.** ANSI-heavy data bypasses
   `canFastWriteOrdinary`. The emulator then runs cell-size, graphics erase,
   OSC 8, and graphics-start processing in addition to the VT parser. The
   offline CPU profile shows meaningful costs in `findGraphicsStart`, OSC 8,
   and query filtering even though this corpus is ordinary styled text.
   Route actual OSC/DCS/APC/query sequences to those features rather than
   repeatedly scanning all styled text. Preserve split-sequence state.
4. **Separate ordered parsing from visual publication.** The GUI's
   `WorkspaceClient.applyTerminalEvent` currently snapshots and invalidates
   for every event. Consume all ordered data, but publish visible snapshots
   at the render cadence, with final/resize handling and low-latency input.
   Never drop output or relax bounded replay/queue limits. This addresses GUI
   overhead beyond the already-slow headless path.
5. **Optimize terminal drawing after the above.** The existing prepared-row
   cache stores text/style runs, but layout still calls `material.Label` for
   each run on each frame. Investigate cached Gio operations or a glyph atlas
   and batched drawing, verifying clipping, scaling, fallback fonts and cell
   alignment. The current profiles do not measure GPU or GUI costs.

PTY reads average approximately 1,022 bytes across roughly 61,134 reads in
this Darwin run. Syscall/scheduling overhead is another candidate after
emulator work. The CPU profile contains native wait/signal stacks; their
sample percentages must not be treated as wall-time components or guaranteed
speedups.

## Ebitengine assessment

Ebitengine offers automatic drawing-command batching and cached rasterized
glyphs, which are suitable building blocks for a terminal-specific renderer.
These are capabilities, not evidence that this corpus would finish faster:
[performance tips](https://ebitengine.org/en/documents/performancetips.html),
[text/v2 glyph cache](https://pkg.go.dev/github.com/hajimehoshi/ebiten/v2/text/v2#CacheGlyphs).

Its current IME package supports macOS and other desktop platforms but remains
experimental. A migration would still need terminal composition, clipboard,
selection, window/UI, graphics, and control-path parity checks:
[exp/textinput](https://pkg.go.dev/github.com/hajimehoshi/ebiten/v2/exp/textinput).

The implemented results support retaining Gio for this workload. A future
Gio/Ebitengine comparison should use identical replay, geometry, fonts,
scrollback, and final-byte completion criteria, with GUI/GPU profiles to show
that rendering is the limiting stage before investing in a renderer migration.

## Reproduction and local artifacts

After settings, split layout, configurable shortcuts and theme integration,
the same native GUI probe reports **0.648 / 0.630 / 0.675 s** `real` (median
**0.648 s**). The probe again checks client consumption through the final
server output sequence. This is a separate idle-machine run after functional
regression tests, using the original script unchanged. GUI artifacts are in
`/tmp/water-cat-perf.q8mxj_m1`.

```sh
WATER_GO_BENCH_COMMAND='cat /Users/clearain/tmp/computer_use/benchmark.data' \
  go test ./internal/gobench -run '^$' \
  -bench 'BenchmarkTerminal(Direct|Server)64MB$' \
  -benchtime=1x -count=3 -benchmem
```

- CPU profile: `/tmp/water-testtermcat.cpu.pprof`
- Allocation profile: `/tmp/water-testtermcat.mem.pprof`
- Profiled test binary: `/tmp/water-testtermcat-bench.test`
- Offline probe source: `target/testtermcat-probe/main.go` (ignored build area)
- Offline binary: `/tmp/water-testtermcat-offline`
- Offline CPU profile: `/tmp/water-testtermcat-offline.cpu.pprof` (final optimized run)
- Native GUI probe: `target/testtermcat-probe/gui.py` (ignored build area)

Optimization acceptance should use this exact corpus plus existing VT/graphics/
OSC 8/split-sequence tests, bounded stream checks, interaction latency, and
real GUI validation. Do not promise a 0.8-second result before measuring it.
