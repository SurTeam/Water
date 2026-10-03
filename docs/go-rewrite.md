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
- Session writes run on one worker with a 64-frame / 16 MiB queue and a bounded
  write deadline. GUI command dispatch performs no socket I/O. Saturation
  explicitly disconnects the session; full event queues and pending calls are
  awakened by close, and the window projects the disconnected state.
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
- Settings panel (`cmd-,` or the Settings button), with terminal/font, pane/UI,
  shortcut, theme/16-color ANSI palette, and startup/shell/server sections.
  Saves are asynchronous and atomic, retaining unknown fields and the existing
  override envelope. Appearance/shortcuts apply across Local and remote views;
  startup, server and history settings require restart.
- Split buttons and configured split-right/split-down keys (`cmd-\\`, `cmd--`),
  directional focus, pane promotion/close, tab cycling, numbered tab bindings
  (`cmd-1` through `cmd-9`, `cmd-0`), workspace cycling and sidebar toggling.
  Dividers preview locally during drag and commit through `pane.resize_split`.
- Live font/geometry/theme changes invalidate cached resolved colors and resize
  the client emulator and PTY through the existing ordered command path.
- Core Rust-compatible theme fields include selection, inverse default colors,
  inactive cursors, pane borders, tab surfaces and sidebar cards. UI fonts read
  Rust's `font_size` / `font_family` keys and the earlier Go `ui_font_size` alias.
- Real `ui.click` routing through Gio's input router rather than a hand-coded
  logical acknowledgement.
- `water ctl ui drag` routes raw press/move/release through Gio's router; the
  native settings smoke test uses the same editor and divider input paths.
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
  stable Rust, 64 MB decoded payload) after Rust-style PTY micro-burst batching,
  exact-sized retained output batches, and the in-tree xterm sparse-map/lazy
  attribute fixes:
  - Plain text: Go direct median **20.33 MB/s**, Go server-local **19.24 MB/s**;
    Rust direct **37.72 MB/s** and Rust server-local **37.11 MB/s**. Go is about
    54% of Rust direct throughput on this pure-text single-pane workload, while
    the Go server path retains about 95% of Go direct throughput.
  - ANSI-heavy: Go direct **22.59 MB/s**, Go server-local **22.09 MB/s**;
    Rust direct **23.71 MB/s** and Rust server-local **21.25 MB/s**. The two
    implementations are effectively in the same hosted-runner range here.
  - Unicode-heavy: Go direct **32.98 MB/s**, Go server-local **30.54 MB/s**;
    Rust direct **42.35 MB/s** and Rust server-local **37.94 MB/s**. Go reaches
    roughly 78-81% of the corresponding Rust path.
  - Plain-text Go allocations are now about **23k allocs/op and 96 MB/op**
    direct, and about **36k allocs/op and 171 MB/op** server-local. Earlier
    versions of the rewrite were around 8.6 million allocs/op and
    0.6-0.8 GB/op.
  - The latest Go allocation profile attributes roughly 79 MB of a ~102 MB
    direct run to immutable retained PTY output batches and about 9.5 MB to
    xterm buffer-line cloning; the former is now close to the unavoidable
    64 MB decoded payload plus bounded batching/replay overhead.
  - Go interaction-under-flood remained low-latency: latest samples were about
    **0.44-0.59 ms** resize and **0.41-1.90 ms** Ctrl-C-to-exit.
  - Four-pane sustained throughput is also close to the Rust oracle: Go direct
    median **26.37 MB/s** and Go server-local **24.68 MB/s**, versus Rust
    **27.20 MB/s** direct and **23.52 MB/s** server-local. The same run measured
    Go direct around **182-210 MiB RSS** with roughly **27 ms p95 / 30-31 ms
    p99** visible-frame gaps, and Go server-local around **227-310 MiB RSS**
    with roughly **33-35 ms p95 / 38-41 ms p99** gaps.
  These hosted-runner numbers are directional rather than release targets, but
  they make throughput, allocation, multi-pane memory, and frame regressions
  visible in CI.

## Remaining parity work

### Native macOS verification, 2026-10-03

- Core ANSI/PTY optimization now reduces the exact local `testTermCat` workload
  from 1.879/1.933 s to 0.792/0.799 s median in matched direct/server headless
  runs. The native GUI runs the unchanged script in 0.753/0.685/0.653 s `real`;
  its client consumed sequence is verified through the final marker. See
  [testtermcat-performance.md](testtermcat-performance.md) for measurement
  boundaries, allocation changes, bounded reader batching/resize barriers,
  validation, and remaining GUI/Linux verification limits.
- Restored the module checksum lockfile and indirect dependency declarations;
  repaired malformed OSC 8 escape literals in the VT regression tests.
- Full Go tests, `go vet`, client/server/PTY/VT/UI race checks, and all four
  existing scenarios pass on native Darwin arm64 with Go 1.27.1.
- Default throughput producers now buffer identical text through
  `dd obs=65536`. BSD `yes` otherwise produces tiny PTY reads that measure
  generator/kernel overhead rather than terminal processing. The 32 MB gate
  also verifies that both paths receive the complete payload before Exit.
  Historical throughput numbers above used the previous producer and should
  not be compared directly with the buffered default. Command overrides still
  permit the original producer for matched Rust/Go comparisons.
- Scenario and Go UI smoke harnesses use unique short `/tmp` paths to avoid
  Darwin Unix-socket path limits under long system/runner temporary paths.
- Native Darwin arm64 GUI smoke now **passes** in an awake desktop session:
  a real Gio frame exposes hit geometry, `ui.click` creates a workspace and
  opens the remote form, control keyboard input reaches the PTY, and a
  1920×1280 PNG screenshot renders successfully. The earlier sleeping-display
  display-link failure remains an environment limitation, not a passing gate.
- The Go GUI smoke now runs on macOS and Linux through Water's own control
  API, with no system input injection. Terminal control keys use Gio's focused
  `TerminalInput` handler; regression coverage verifies mixed-case/UTF-8 text,
  application-cursor mode, modified keys, and function keys. Local GUI
  connection identities now use complete random UUIDv4 values.
- Configured workspace/tab/pane shortcuts now also run from real focused Gio
  key events, through the same command dispatch used by control automation.
  Handled shortcuts are consumed before terminal encoding; unmatched terminal
  keys continue through the normal input handler.
- Native OS clipboard and system IME behavior remain manual validation gates;
  control input does not establish native input-method or clipboard parity.
- Settings/split regression also passes in the native window: save and reload
  font/line height/sidebar width/colors, preserve unknown configuration fields,
  apply a custom split shortcut, drag a divider, promote a pane, switch numbered
  tabs/workspaces, and cancel the settings dialog. Unit coverage additionally
  checks editor focus across control/native routers and edits arriving in the
  same input batch as Save. Reproduce after building `target/go-ui-smoke/water`:

  ```sh
  caffeinate -du ~/.venv/bin/python scripts/go-ui-settings-smoke.py
  ```

  On Linux, omit `caffeinate` and use an existing display session. The macOS
  wrapper holds a temporary display-awake assertion for the test lifetime:
  this machine reported zero active displays after idle sleep, which prevents
  Gio from creating its display link. It does not inject GUI input.
- After settings/split integration, the unchanged `testTermCat` script reports
  0.648 / 0.630 / 0.675 s `real` (median 0.648 s), with the client sequence
  verified through the final marker. Full Go tests, `go vet`, affected-package
  race tests, all four scenarios and the existing GUI smoke pass again.

The automated rewrite gates now cover the headless protocol, terminal stream,
scenario suite, performance smoke tests, selection/scrollback behavior,
graphics, multi-connection remote transport, Linux/macOS package construction,
bidirectional Rust/Go control+WT4 compatibility, and real-window
control-input/click/screenshot behavior. Earlier cross-language Xvfb harnesses
also exercised keyboard/clipboard behavior; the portable Go-only smoke does
not substitute for native OS clipboard validation.

Only credentialed/manual environment validation remains:

- **Native clipboard session.** Select and copy terminal text, paste through
  the configured platform shortcut, and verify the exact UTF-8 result in the
  PTY. The automated input handler tests cover clipboard transfer and
  bracketed-paste framing, but the portable smoke does not drive the OS
  clipboard or claim this manual gate has passed.

- **Native system IME session.** Deterministic Gio IME state-machine coverage is
  automated, including caret/snippet publication, preedit isolation/rendering,
  and single final commit delivery. Xvfb cannot reliably drive a native input
  method engine, so execute a real desktop composition session with:

  ```sh
  bash scripts/run-go-ime-manual.sh
  ```

  The harness starts the Go GUI/server, switches the active PTY to `cat`,
  and performs two phases: it first captures any non-empty native preedit in
  `ui.snapshot` (including phonetic Pinyin/Kana-style preedit) while asserting
  those bytes have not leaked into the PTY, then prompts for commit and verifies
  the configured final UTF-8 probe text in the authoritative server-owned
  terminal stream plus composition-state cleanup. Override the final probe for
  a different IME with `WATER_IME_PROBE_TEXT='…'`.

- **Credentialed Apple signing/notarization.** The signing workflow now performs
  a fail-fast credential/tool preflight before downloading the unsigned asset.
  A signed run requires the repository code-signing secrets
  `SURTEAM_CODE_P12_BASE64` and `SURTEAM_SIGN_PASS` (plus
  `SURTEAM_SIGNING_IDENTITY` only when an explicit identity override is
  desired). A notarized run additionally requires
  `APPLE_NOTARY_KEY_BASE64`, `APPLE_NOTARY_KEY_ID`, and
  `APPLE_NOTARY_ISSUER_ID`.

  Stage an unsigned Go bundle and dispatch the signing workflow from
  `go-rewrite` with:

  ```sh
  CODESIGN_SKIP=1 WATER_APP_VARIANT=dev bash scripts/build-go-macos-app.sh
  WATER_APP_VARIANT=dev WATER_RELEASE_PUBLICATION=none \
    WATER_SIGNING_WORKFLOW_REF=go-rewrite \
    bash scripts/publish-unsigned-macos.sh
  ```

  Then run the dispatched workflow with notarization enabled. The workflow
  imports the certificate into an isolated temporary keychain, signs nested
  executables before the app bundle, submits through `notarytool`, staples
  the ticket, re-extracts the final archive, and verifies the signed/stapled
  bundle before publication.

Do not merge `go-rewrite` into `main` until the credentialed Apple path,
native clipboard, and one true system-IME desktop session are executed or
explicitly waived. All
other protocol, scenario, performance, packaging, cross-language, and
real-window compatibility gates are automated.
