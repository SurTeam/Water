# Go rewrite branch

The `go-rewrite` branch keeps the Rust implementation as a behavioral and
benchmark oracle while replacing both the Water client and server in Go.

## Compatibility contract

- Binary terminal data plane stays protocol v4 with the `\0WT4` prefix.
- The current control API signature is `water-control/v5`.
- JSON control frames keep the existing 32-bit big-endian length prefix.
- Live terminal events keep the existing UUID / sequence / geometry layout.
- The server owns PTYs and bounded replay; clients own VT emulation and
  presentation state.

## Selected Go stack

- Go 1.27.
- PTY: `github.com/creack/pty`.
- VT emulator: `github.com/gitpod-io/xterm-go`, isolated behind
  `internal/govt` so it can be swapped without touching protocol code.
- Desktop UI: Gio 0.10.3.

## Implemented baseline

- Unix-domain Go control server.
- WT4 binary frame read/write.
- PTY spawn, raw output, input, resize, exit sequencing.
- Bounded 8 MiB replay.
- Live attach/detach fanout.
- Terminal command dispatch with operation snapshots.
- Go short-lived RPC client and long-lived session client.
- Headless xterm-go emulator wrapper.
- Initial Gio terminal window.
- Go CLI for info/ping/spawn/send/resize/attach.
- Protocol round-trip test and branch CI.

## Remaining parity work

The branch is intentionally not merge-ready yet. Full parity still requires
the workspace/tab/pane model, remote SSH deployment, agent detection,
complete control CLI, styled cell renderer, input/IME/mouse/selection,
OSC 8 and graphics, UI automation, packaging/signing, and parity/performance
validation against the Rust implementation.
