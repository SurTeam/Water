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
- VT core: `github.com/gitpod-io/xterm-go`, isolated behind
  `internal/govt` so Water can replace it without changing the wire protocol.
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
  IME composition, bracketed paste, DEC mouse tracking, and local scrollback.
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
- The ordinary macOS CI smoke-builds the unsigned Go app bundle and archive.
  The signing workflow can optionally submit the signed bundle through
  `notarytool`, staple the ticket, and validate it before release packaging.
- Hot-path counters for PTY reads, terminal stream bytes/events, replay,
  snapshot pushes, and client processing.
- `ui.snapshot` reports GUI-local terminal diagnostics separately from server
  memory: attached terminals, visible cells, prepared-row cache entries, Gio
  image-texture cache entries, and decoded terminal-graphics bytes.

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
- `.github/workflows/go-rust-benchmark.yml` provides a manual same-runner
  Rust-vs-Go 64 MB comparison using the existing Rust completion benchmark and
  Go direct/server benchmarks, plus interaction-under-flood measurements. Raw
  logs are retained as a workflow artifact.
- Example shared-runner measurements observed while adding the gate:
  - 26.8 MB/s direct vs 23.6 MB/s server (87.8% retention).
  - 16.1 MB/s direct vs 13.0 MB/s server (80.6% retention) under a noisier run.
  - 15.8 MB/s direct vs 12.0 MB/s server (76.0% retention) on the latest
    shared-runner gate; both decoded queues reached the intended 64-event cap.
  - Latest interaction gate: 0.49 ms resize and 0.98 ms Ctrl-C-to-exit during
    output flood.
  These are CI smoke measurements, not substitutes for the formal same-machine
  Rust-vs-Go benchmark.

## Remaining parity work

The automated rewrite gates now cover the headless protocol, terminal stream,
scenario suite, performance smoke tests, selection/scrollback behavior,
graphics, single-connection remote transport, and macOS bundle construction.
One functional parity gap remains alongside the credentialed/manual validation
work:

- Port Rust's multi-connection desktop model: one window can keep Local plus
  multiple independent SSH connections alive, switch between their workspace
  projections, connect/disconnect remotes dynamically, and expose the combined
  connection list. The current Go GUI can connect to SSH, but a launch still
  owns only one active control/session plane.
- Add or run GUI-only Rust/Go compatibility scenarios that require a real
  desktop window, especially end-to-end pointer/IME/clipboard interactions.
- Run the manual same-runner Rust-vs-Go benchmark workflow and publish the
  resulting 64 MB completion/interaction data. For release decisions, also run
  the larger ANSI-heavy / sustained-memory fixtures and record p50/p95/p99,
  allocations, and RSS rather than relying only on shared-runner smoke gates.
- Configure the repository's Apple notarization API-key secrets
  (`APPLE_NOTARY_KEY_BASE64`, `APPLE_NOTARY_KEY_ID`,
  `APPLE_NOTARY_ISSUER_ID`) and execute one signed notarized release/dev
  artifact to validate the credentialed path. The workflow implementation is
  present, but CI cannot validate Apple credentials without those secrets.
- Decide whether server `debug.memory` should aggregate GUI-local cache
  diagnostics across processes. The GUI already exposes accurate local cache
  counts through `ui.snapshot`; the headless server intentionally does not
  invent those values.

Do not merge `go-rewrite` into `main` until the credentialed release path,
manual same-runner benchmark, and any required real-window compatibility
scenarios are executed or explicitly waived.
