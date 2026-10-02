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
- The server owns PTYs and bounded replay; clients own VT emulation,
  viewport state, selection/presentation state, and terminal query replies.

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
  replay, attach/detach, subscriber backpressure, and replay resync.
- UUID operation registry with bounded history.
- Revisioned application state and `push.snapshot` GUI sessions.
- Workspace, tab, pane-tree, surface, terminal, pane movement/promotion,
  directional focus, resize, rename, and lifecycle commands.
- Event history, agent lifecycle events, debug memory/metrics, connection
  projection, UI request forwarding, and graceful shutdown.
- Transactional terminal replacement so stale process exits cannot close a
  newly replaced surface.

### Client and UI

- Short-lived RPC client and long-lived GUI session client.
- Headless xterm-go emulator with 256/RGB colors, cell attributes, wide cells,
  OSC hyperlink IDs, alternate-screen state, row hashes, scrollback, and
  terminal title tracking.
- VT-generated DA/DSR/mouse replies are sent back to the PTY.
- Gio workspace/sidebar/tab/pane UI backed by server snapshot pushes.
- Cached row/style-run terminal renderer instead of rebuilding the whole
  terminal as one string.
- Keyboard input, Ctrl/Alt sequences, function keys, application-cursor mode,
  IME composition, bracketed paste, DEC mouse tracking, and local scrollback.
- Window-driven PTY resize.
- Real offscreen Gio screenshot rendering through `gpu/headless`.

### Remote, config, CLI, and diagnostics

- Config loading/defaults for startup, server lifecycle, shell, terminal,
  features, shortcuts, and theme.
- OpenSSH ControlMaster remote tunnel and embedded Go server payload build.
- Agent detection/projection.
- `water ctl` Go command surface for state/info/server/debug/connections,
  workspace/tab/pane/surface/terminal/operation/UI/scenario controls.
- Existing Water scenario JSON format is understood by the Go CLI.
- Hot-path counters for PTY reads, terminal stream bytes/events, queues,
  replay, snapshot pushes, and client processing.
- macOS/Linux Go CI, protocol/model/server/client/VT/UI tests, and end-to-end
  real PTY tests.

## Remaining parity work

The branch is not merge-ready until these remaining gaps are closed:

- Make `ui.click` coordinate automation inject a real Gio pointer click
  instead of returning only a logical acknowledgement.
- Complete terminal selection/copy behavior and hyperlink activation/download
  behavior in Gio.
- Add graphics/image protocol rendering parity where the Rust terminal
  supports it.
- Run and gate the full existing scenario suite, including platform-dependent
  zsh and agent fixtures, through the Go CLI/server.
- Add systematic Rust-client ↔ Go-server and Go-client ↔ Rust-server
  compatibility tests for every control method and binary terminal event.
- Close remaining debug-memory accounting gaps such as visible-cell, surface,
  shaping-cache, and image-cache counts.
- Reproduce release packaging/signing/application-bundle workflows for the Go
  binaries.
- Meet the existing terminal streaming latency, throughput, memory, and
  backpressure performance gates before replacing the Rust implementation.

Do not merge `go-rewrite` into `main` until the parity and performance gates
above are green.
