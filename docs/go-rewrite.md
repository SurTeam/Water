# Go rewrite branch

The `go-rewrite` branch keeps the Rust implementation as a behavioral and
benchmark oracle while replacing both the Water client and server in Go.

## Compatibility contract

- Binary terminal data plane stays protocol v4 with the `\0WT4` prefix.
- The current control API signature is `water-control/v5`.
- JSON control frames keep the existing 32-bit big-endian length prefix.
- Live terminal events keep the existing UUID / sequence / geometry layout.
- Operation IDs remain UUIDs.
- PTY output, resize, and exit share one strictly ordered sequence.
- Worker and decoded-client terminal queues are both bounded to 64 events.
- The server owns PTYs and bounded replay; clients own VT emulation,
  viewport state, selection/presentation state, graphics, and terminal query
  replies.

## Go stack

- Go 1.27.
- Unix PTY: `github.com/creack/pty`.
- VT core: a pinned MIT-licensed fork of `github.com/gitpod-io/xterm-go`
  lives under `internal/xterm`, still isolated behind `internal/govt`.
  The in-tree fork currently carries Water's scrollback sparse-map reuse
  optimization and can be removed once upstream provides equivalent behavior.
- Desktop UI: Gio 0.10.3.
- Remote transport: the system OpenSSH client with ControlMaster and
  Unix-socket forwarding, matching the Rust architecture.

## Implemented

### Server and protocol

- Unix-domain control server with version/build compatibility checks.
- WT4 binary terminal output/resize/exit frames.
- PTY spawn, input, resize, strict output-before-exit sequencing, bounded
  replay, attach/detach, 64-event subscriber backpressure, and replay resync.
- UUID operation registry with bounded history.
- Revisioned application state and `push.snapshot` GUI sessions.
- Workspace, tab, pane-tree, surface, terminal, pane movement/promotion,
  directional focus, resize, rename, and lifecycle commands.
- Event history, agent lifecycle events, debug memory/metrics, connection
  projection, UI request forwarding, and graceful shutdown.
- Transactional terminal replacement so stale process exits cannot close a
  newly replaced surface.

### Client and UI

- Short-lived RPC client and long-lived GUI session client with a 64-event
  decoded terminal queue.
- Headless xterm-go emulator with 256/RGB colors, cell attributes, wide cells,
  OSC 8 URIs, alternate-screen state, row hashes, wrapped-row metadata,
  scrollback, and terminal title tracking.
- Attach replay is side-effect free; VT-generated DA/DSR/mouse replies are
  written back to the PTY only for live input.
- Gio workspace/sidebar/tab/pane UI backed by server snapshot pushes.
- One desktop window can keep Local plus multiple independent SSH Water sessions
  alive at once, switch their workspace projections, add remotes at runtime,
  disconnect them without closing the window, and expose one combined
  connection list through GUI automation/control projection.
- Cached row/style-run terminal renderer instead of rebuilding the whole
  terminal as one string.
- Keyboard input, Ctrl/Alt sequences, function keys, application-cursor mode,
  bracketed paste, DEC mouse tracking, and local scrollback.
- Gio IME integration publishes terminal-cursor caret/snippet state to the
  platform input method, keeps preedit text local and visibly overlaid at the
  cursor, and sends only the final committed edit to the PTY.
- Character drag selection, Rust-style repeated-click word/segment expansion,
  CJK segmentation, wide-cell handling, wrapped-line copy semantics, and
  clipboard copy-or-interrupt behavior.
- Selection endpoints use absolute xterm buffer rows, so selections remain
  stable while the viewport moves; edge selection autoscroll runs on scheduled
  Gio invalidations and can extend into off-screen scrollback.
- OSC 8 hyperlink activation with platform-shortcut/Shift selection rules,
  safe target validation, local default-application opening, and remote
  `file://` download through the existing SSH ControlMaster before opening.
- Kitty graphics, iTerm2 inline image, and Sixel parsing/rendering with Gio
  texture caching, ordered cursor effects, erase handling, and cell-pixel
  queries.
- Window-driven PTY resize.
- Real `ui.click` routing through Gio's input router rather than a hand-coded
  logical acknowledgement.
- Real offscreen Gio screenshot rendering through `gpu/headless`.

### Remote, config, CLI, diagnostics, and packaging

- Config loading/defaults for startup, server lifecycle, shell, terminal,
  features, shortcuts, and theme.
- OpenSSH ControlMaster remote tunnel and versioned embedded Go server payloads
  for Darwin/Linux on amd64/arm64.
- Agent detection/projection, including shell `exec -a` aliases.
- `water ctl` Go command surface for state/info/server/debug/connections,
  workspace/tab/pane/surface/terminal/operation/UI/scenario controls.
- Existing Water scenario JSON format is understood by the Go CLI.
- Native macOS Go `.app` packaging with the same Water/Water Dev bundle and
  binary names consumed by the existing codesign/release workflow.
- Native Linux Go tarball packaging with the same dev/release binary names as
  the Rust package (`water-dev` / `water-srv-dev` or
  `water` / `water-server`).
- Ordinary CI smoke-builds both the Linux Go archive and the unsigned macOS Go
  app bundle/archive.
  The signing workflow can optionally submit the signed bundle through
  `notarytool`, staple the ticket, and validate it before release packaging.
- Hot-path counters for PTY reads, terminal stream bytes/events, replay,
  snapshot pushes, and client processing.
- `debug.memory` is intentionally process-local: the server reports only
  PTY/replay/model state it owns. GUI cache diagnostics stay in `ui.snapshot`
  because the client owns VT/render/graphics memory; the server does not block
  on or fabricate cross-process cache values.
- `ui.snapshot` reports GUI-local terminal diagnostics: attached terminals,
  visible cells, prepared-row cache entries, Gio image-texture cache entries,
  and decoded terminal-graphics bytes.

### Validation gates

- macOS/Linux unit, vet, protocol/model/server/client/VT/UI, and real-PTY tests.
- The full existing scenario suite is gated on both Linux and macOS, including
  the zsh and agent fixtures.
- A bidirectional Rust/Go compatibility job starts the opposite-language
  server and exercises typed/generic control RPCs plus the live WT4 stream.
  The covered headless surface includes info/state/events/debug/connections,
  operations, terminal snapshot/replay/contains/wait-exit, attach/detach, and
  command dispatch.
- A dedicated performance job runs a 32 MB direct/server terminal workload,
  verifies sequence continuity, 64-event queue caps, the 8 MiB replay bound,
  and a conservative shared-runner throughput gate. It also measures resize
  and Ctrl-C-to-exit latency while stdout is flooded.
- `.github/workflows/go-rust-benchmark.yml` provides an automatic/manual
  same-runner Rust-vs-Go 64 MB comparison using the existing Rust completion
  benchmark and Go direct/server benchmarks, plus interaction-under-flood
  measurements. Raw logs and a Go allocation profile are retained as workflow
  artifacts.
- Example shared-runner measurements observed while adding the gate:
  - 26.8 MB/s direct vs 23.6 MB/s server (87.8% retention).
  - 16.1 MB/s direct vs 13.0 MB/s server (80.6% retention) under a noisier run.
  - 15.8 MB/s direct vs 12.0 MB/s server (76.0% retention) on the latest
    shared-runner gate; both decoded queues reached the intended 64-event cap.
  - Latest interaction gate: 0.49 ms resize and 0.98 ms Ctrl-C-to-exit during
    output flood.
  These are CI smoke measurements, not substitutes for the formal same-machine
  Rust-vs-Go benchmark.
- Latest same-runner language benchmark (Ubuntu hosted runner, Go 1.27.x,
  stable Rust, 64 MB decoded payload) after PTY micro-burst batching and the
  in-tree xterm sparse-map reuse fix:
  - Plain text: Go direct median ~17.6 MB/s, Go server-local ~17.5 MB/s;
    Rust direct ~23.1 MB/s and Rust server-local ~23.2 MB/s. Go retains about
    76% of Rust direct completion throughput while its server path retains
    essentially all of Go direct throughput.
  - ANSI-heavy: Go direct median ~15.4 MB/s, Go server-local ~15.0 MB/s;
    Rust direct ~18.2 MB/s and Rust server-local ~18.2 MB/s.
  - Unicode-heavy: Go direct median ~21.4 MB/s, Go server-local ~22.4 MB/s;
    Rust direct ~32.5 MB/s and Rust server-local ~30.2 MB/s.
  - Plain-text Go allocations fell from roughly 8.6 million allocs/op and
    0.6–0.8 GB/op to about 44 thousand allocs/op and 0.10–0.13 GB/op direct
    (about 50 thousand allocs/op and 0.18–0.19 GB/op server-local).
  - Go interaction-under-flood remained low-latency: resize stayed around
    0.9–1.1 ms in the latest samples and Ctrl-C-to-exit stayed below 6 ms.
  - Four-pane sustained throughput/RSS/frame cadence is also automated now.
    On the latest same-runner sample, Go direct completed at about
    16.2–16.6 MB/s aggregate with 375–382 MiB RSS and roughly
    35–37 ms p95 / 45–47 ms p99 visible-frame gaps; Go server-local completed
    at about 17.7–19.1 MB/s with 60–64 MiB RSS and roughly 23 ms p95 /
    24–25 ms p99 gaps. The Rust oracle measured about 20.14 MB/s direct with
    168.6 MiB RSS and 24.3/29.3 ms p95/p99, and about 19.38 MB/s server-local
    with 211.4 MiB RSS and 24.7/32.4 ms p95/p99. These hosted-runner numbers
    are directional rather than release targets, but they make multi-pane
    memory/frame regressions visible in CI.

## Remaining parity work

The automated rewrite gates now cover the headless protocol, terminal stream,
scenario suite, performance smoke tests, selection/scrollback behavior,
graphics, multi-connection remote transport, macOS bundle construction, and
real-window Gio automation under Xvfb.

Remaining work is credentialed/manual validation plus a small set of desktop
interop cases:

- Keep the real-window cross-language compatibility gates green in both
  directions: Go GUI against the Rust server and Rust GUI against the Go
  server. These exercise UI registration, state mutation through real window
  input, and screenshot capture over the opposite-language control plane.
- Keep the real-window keyboard/clipboard compatibility paths green in both
  directions. Deterministic Gio IME state-machine coverage is automated; a
  true system-IME composition session still needs platform-specific manual
  validation because Xvfb cannot reliably drive a native input-method engine.
- Configure the repository's Apple notarization API-key secrets
  (`APPLE_NOTARY_KEY_BASE64`, `APPLE_NOTARY_KEY_ID`,
  `APPLE_NOTARY_ISSUER_ID`) and execute one signed notarized release/dev
  artifact to validate the credentialed path. The staging helper now dispatches
  the signing workflow from the current branch by default (override with
  `WATER_SIGNING_WORKFLOW_REF`), so this can be validated on `go-rewrite`
  before merging. CI still cannot validate Apple credentials without those
  secrets.

Do not merge `go-rewrite` into `main` until the credentialed Apple release
path and true system-IME/manual desktop checks are executed or explicitly
waived. The same-runner completion, interaction, multi-pane/RSS/frame-time,
headless compatibility, scenario, and bidirectional real-window gates are now
automated.
