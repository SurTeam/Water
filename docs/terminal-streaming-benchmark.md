# Terminal streaming benchmark — 2026-09-10

## Scope

Release-mode comparison of the in-process direct PTY path and the separated
local server path on the same machine, with the same 80×24 terminal and 10,000
line scrollback. Each formal workload uses 3 warmups followed by 10 measured
runs. Completion starts when the attached terminal receives the command and
ends after the final sequence has been applied to the client emulator and its
final immutable snapshot has been built.

The benchmark also records the authoritative process-exit notification,
emulator catch-up, visible snapshot cadence, and observed queue high-water
marks. `backlogged_visible_gap` only includes intervals where another event
was already queued after the current event; `visible_gap` also includes gaps
where the producer/PTY had not supplied another event.

## Results

| Workload | Metric | Direct | Server local | Server/direct or retention |
|---|---:|---:|---:|---:|
| 61.4 MB ANSI-heavy `testTermCat` | process median | 649.190 ms | 719.545 ms | 1.108× |
|  | visible median | 650.795 ms | 712.636 ms | 1.095× |
|  | throughput retention | — | — | 91.3% |
|  | catch-up p95 | 29.961 ms | 0 ms | — |
|  | backlog gap p95 / p99 / max | 16.603 / 16.642 / 16.658 ms | 16.671 / 16.708 / 16.708 ms | — |
|  | queue peak | — | server 1,114,112 B / 17 events; client 4,259,840 B / 65 events | bounded |
| 61.4 MB newline-heavy | process median | 3,124.436 ms | 3,086.671 ms | 0.988× |
|  | visible median | 3,117.950 ms | 3,080.555 ms | 0.988× |
|  | throughput retention | — | — | 101.2% |
|  | catch-up p95 | 0 ms | 0 ms | — |
| 500 MB ANSI-heavy input | process median | 5,212.729 ms | 5,436.315 ms | 1.043× |
|  | visible median | 5,205.949 ms | 5,429.552 ms | 1.043× |
|  | throughput retention | — | — | 95.9% |
|  | catch-up p95 | 0 ms | 0 ms | — |
|  | backlog gap p95 / p99 / max | 16.611 / 16.698 / 17.375 ms | 16.613 / 16.684 / 16.739 ms | — |
|  | queue peak | — | server 147,810 B / 6 events; client 1,114,112 B / 17 events | bounded |

The 500 MB workload sends eight complete copies of the 61.4 MB ANSI fixture
plus its first 8.8 MB, for exactly 500,000,000 input bytes. PTY output is about
509 MB because terminal output processing expands line endings.
The formal 500 MB run's observed downstream queues were already below the
later 64-event caps. A post-cap verification run measured 5.332 s direct and
5.773 s server (1.083×, 92.4% retention), zero catch-up, and a 640 KiB / 10
event client peak.

### Interaction during `yes` flood

| Metric | Direct p50 / p95 / p99 | Server local p50 / p95 / p99 |
|---|---:|---:|
| resize event latency | 0.096 / 0.149 / 0.149 ms | 0.290 / 0.351 / 0.351 ms |
| Ctrl-C to final exit sequence | 24.584 / 24.725 / 24.725 ms | 24.738 / 25.008 / 25.008 ms |

## Architecture changes measured

- Protocol version 3 keeps JSON for control messages and attach replay but
  sends live terminal output in ordered binary frames with raw PTY bytes.
- The client reader now delivers decoded raw events directly to the emulator;
  live output no longer incurs Base64 encode/decode or terminal-event JSON.
- Terminal parsing and snapshot construction remain off the GPUI thread.
- The former backlog-triggered detach/replay path was removed; it could cause
  an 8 MiB Base64/JSON replay pause and lose scrollback older than the replay
  ring even though the live sequence was complete.
- Worker-attachment and decoded-client queues are each bounded to 64 events
  (roughly 4 MiB at the shared 64 KiB maximum event size).
- Queue gauges/high-water marks are exposed by `debug.metrics`.
- Blocking terminal contains/exit waits now run outside the model thread, so
  they cannot prevent input or resize commands from reaching the PTY worker.

## Validation (2026-09-10 historical run)

- `cargo test --all-targets`: passed in the historical run recorded here.
- `cargo clippy --all-targets`: passed with existing warnings only.
- `cargo build --release`: passed.
- Binary output, resize, exit, mixed JSON/session framing, detach/reattach,
  replay, scrollback, selection, and real-shell tests passed.

## PTY reader idle regression — 2026-09-12

The reader regression tests run the production PTY reader and expose these
diagnostic counters through `debug.metrics`: poll wakeups, zero-event and
spurious wakes, PTY-ready wakes, empty readiness, `WouldBlock` reads, and
empty-wake backoff activations. The reader starts in an IDLE blocking wait,
clears `Events` before each wait, enters DRAIN only for an actual PTY event,
and treats a first-read `WouldBlock` as the end of that drain. A successful
read resets the empty-wake streak; repeated empty readiness backs off from
1 ms to an 8 ms cap. The successful-data micro-burst remains bounded by both
1 ms and 64 `WouldBlock` probes.

The following measurements were taken on Darwin 25 / arm64 with the current
debug test binary. The Linux path was not runnable in this environment.

| Workload | CPU time | empty readiness | `WouldBlock` reads |
|---|---:|---:|---:|
| 1 idle PTY / 3 s | 4 ms | 0 | 0 |
| 1 / 4 / 8 idle PTYs / 1 s each | 3 / 5 / 4 ms | 0 / 0 / 0 | 99 / 132 / 128 |
| 10 ms trickle / 2.5 s | 13 ms | 0 | 8,293 |

The measured idle → active wake latency was 0.422 ms.

`sample` during the idle test found both `water-terminal-reader` and its
worker blocked in `polling::Poller::wait_impl -> kevent`; no stable phantom
readiness storm was reproducible on this macOS host. A pre-cap local trickle
run recorded about 139,450 `WouldBlock` probes; the 64-probe cap reduced that
to 8,293 in the latest run without changing the byte-delivery result. The
deterministic reader state tests cover zero-event gating and repeated
empty-readiness backoff on all platforms; Linux profiling and runtime PTY
validation remain pending.

The current targeted run passed all 10 reader tests in 11.81 s. The current
`cargo test --all-targets` run passed the library tests (149 passed, 1 ignored)
but remains blocked by the unrelated `build_variants` fixture, which does not
copy `.agents/skills/water-control/SKILL.md` into its temporary package.

```sh
# Fast reader regression suite (the test binary is already compiled)
./target/debug/deps/terminal_reader-* --test-threads=1 --nocapture
```

## SSH status

No trustworthy same-machine SSH result is available in this environment.
`localhost` SSH is not enabled. One configured host accepts non-interactive
SSH, but it is a different Linux x86_64 machine and lacks both the benchmark
fixture and a matching Water server, so its numbers would violate the required
same-machine/same-input comparison. The SSH path is a Unix-socket forward and
therefore carries the same version-3 binary frames, but it still needs a
properly configured same-machine SSH target for formal measurement.

## Commands

```sh
# 61.4 MB ANSI-heavy fixture
WATER_BENCH=1 WATER_BENCH_WARMUPS=3 WATER_BENCH_RUNS=10 \
  cargo test --release --test bench_terminal bench_completion_parity \
  -- --ignored --nocapture

# newline-heavy
WATER_BENCH=1 WATER_BENCH_WARMUPS=3 WATER_BENCH_RUNS=10 \
  WATER_BENCH_COMMAND='yes WATER_NEWLINE_HEAVY | head -c 61400000' \
  cargo test --release --test bench_terminal bench_completion_parity \
  -- --ignored --nocapture

# interaction under stdout flood
WATER_BENCH=1 WATER_BENCH_WARMUPS=3 WATER_BENCH_RUNS=10 \
  cargo test --release --test bench_terminal bench_interaction_parity \
  -- --ignored --nocapture
```
