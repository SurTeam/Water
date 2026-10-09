# Water documents

This is the operational reference for contributors and users who need commands. The product overview is in [README.md](../README.md); implementation boundaries are in [ARCHITECTURE.md](../ARCHITECTURE.md); non-negotiable editing and release rules are in [AGENTS.md](../AGENTS.md).

Current release: [0.4.2](releases/v0.4.2.md). GUI performance measurements and their limits are recorded in [the CPU pipeline report](cpu-pipeline-profile-2026-10-04.md), [the memory and input report](memory-input-profile-2026-10-04.md), [the GUI memory report](gui-memory-fix-2026-10-05.md), and [the scrolling optimization report](scrolling-optimization-2026-10-05.md).

Server Web direct access, GUI/SSH QR pairing, TLS configuration and `water ctl server web` commands are documented in [Server Web access](server-web.md).

## Requirements

Use the Go version specified in `go.mod`. Native macOS GUI builds need Xcode Command Line Tools; app packaging also uses `codesign`, `plutil`, `ditto` and `unzip`. Linux GUI builds need the Ebitengine/Gio native graphics dependencies and an available display/GPU. Remote connections use OpenSSH. Python smoke scripts use `~/.venv/bin/python`.

## Run the Go GUI (Ebitengine)

The Go GUI uses Ebitengine with a custom titlebar, window controls, resizing, terminal renderer and settings panels. It requires a native desktop display. Installed terminal font families are resolved from the system font directories; bundled Go fonts provide the fallback.

```sh
mkdir -p target/go-ui-smoke
go build -o target/go-ui-smoke/water ./cmd/water
go build -o target/go-ui-smoke/water-server ./cmd/water-server
target/go-ui-smoke/water

# Real native-window checks through Water's control API, in owned instances.
bash scripts/run-go-ui-smoke.sh
WATER_BIN="$PWD/target/go-ui-smoke/water" ~/.venv/bin/python scripts/go-ui-settings-smoke.py
WATER_BIN="$PWD/target/go-ui-smoke/water" ~/.venv/bin/python scripts/go-ui-lifecycle-smoke.py
WATER_BIN="$PWD/target/go-ui-smoke/water" ~/.venv/bin/python scripts/go-ui-parity-smoke.py
WATER_BIN="$PWD/target/go-ui-smoke/water" ~/.venv/bin/python scripts/go-ui-language-grid-smoke.py

# Continuous output with real application switching and increasing background intervals.
~/.venv/bin/python scripts/go-ui-background-smoke.py
```

The Go frontend supports English and Simplified Chinese. Choose **UI → Language**
in Settings, then Save; the preference also updates Water's native macOS menu.
UI labels, setting names, validation hints and dialogs are localized. Terminal
output, user-provided workspace/tab names and remote error details remain content
from their source. `ui.language` is `en` or `zh-Hans` (the loader also accepts
`zh` and `zh-CN`). Language selection previews the UI and Cancel restores the
saved preference.

Startup window dimensions are terminal cells: `startup.window_columns` and
`window_rows`, with `window_min_columns` and `window_min_rows` for the minimum.
The client calculates pixels from the resolved font, line height, display scale,
titlebar, sidebar and padding. Startup dimensions also supply the default new terminal
size; legacy `terminal.default_columns`/`default_lines` migrate only when the
startup values are absent and are removed on save. Tab captions follow the active
pane's foreground command unless renamed. `ui.tab_font_size` controls their font
independently; `ui.tab_max_title_length` limits captions (default 32 characters).
Tabs remain in the titlebar when the sidebar is hidden and start after the
window controls. Each tab measures its caption with one space on each side,
plus the configured tab padding; overflow scrolls horizontally.
Settings categories have section headings. Window popups dim the background
without a drop shadow; stacked panels do not dim it twice.
These settings affect new windows. Older pixel
fields are accepted but ignored by the Go frontend and removed when its Settings
page saves. They are not converted to cells, since the old dimensions did not
identify the font or display scale. Manually resized panes center the fraction
of a cell left over at each edge; the default terminal inset is 2 dp (existing
explicit `ui.pane_padding` settings are respected). Buttons center their measured
text in both axes.
Logical Unicode widths match shell layout: private-use Nerd Font symbols such
as `` count as one column; CJK and wide emoji count as two. An icon can draw
into a following blank cell of the same background without consuming an extra
logical column. Beside text or at the right edge it fits its own cell.
Glyphs use their actual ink bounds, scale down proportionally when necessary,
and center in their available drawing space.
Combining marks remain part of their base cell; selection, wrapping and the
block cursor follow the same logical columns and drawing policy.
Drag selection uses character-cell midpoints to choose insertion boundaries.
Shift-click preserves the existing anchor and moves the other endpoint, including
across the anchor. Selection highlights complete two-cell glyphs even when a
drag endpoint touches only one half. Copying uses the same boundaries, including icon drawing space
borrowed from a following blank, without changing shell column calculations.
The language/grid smoke checks actual shell dimensions, both languages and native
menu titles, persisted settings and restart, different font sizes and sidebar
visibility, and captures real rendered screenshots using the control API.

Hyperlink targets resolve from the terminal's own OSC8 registry, which also
assigns each cell's link ID. Drawing and clicking use the same glyph footprint.
`scripts/go-ui-hyperlink-smoke.py` checks erased/reused links, adjacent rows,
both halves of wide glyphs, and the final two visible linked rows of the actual
`ls ~/Documents` output. Its isolated GUI captures opener arguments with a
temporary test executable instead of launching files or a browser.

OSC 4/10/11/12 query, set and restore colors use the configured palette and
foreground/background/cursor colors. Application overrides affect rendering
and subsequent queries; OSC 104/110/111/112 restore the theme defaults.
`scripts/go-ui-tabs-colors-smoke.py` verifies title-sized tabs, sidebar hiding
and actual Codex composer background pixels without submitting a prompt.

Additional client-side terminal reports are available through the real PTY:

| Sequence | Behavior |
| --- | --- |
| CSI 18 t | Current terminal rows and columns, including after a split/resize |
| CSI 14 t / 14;2 t | Text grid / complete native window size in physical pixels |
| CSI 15 t / 16 t / 19 t | Monitor pixels / character-cell pixels / monitor capacity in cells |
| CSI 11 t / 13 t / 13;2 t | Window state / window position / text grid position |
| CSI 5 n / 6 n / ?6 n | Terminal status and current cursor position; honors origin mode |
| CSI 20 t / 21 t | OSC icon label / window title replies; title stacks use CSI 22/23 t |
| CSI ?996 n / ?2031 h/l | Light/dark query and configured-theme change notifications |
| DCS $q ... ST | Current SGR attributes, cursor style, protection and scroll margins |
| DCS +q ... ST | Hex-encoded terminal-name, 256-color and direct-RGB capability queries |
| OSC 7 ; file URI ST | Current directory metadata, exposed per grid by `water ctl ui snapshot` |
| OSC 52 ; c ; base64 ST | Copy UTF-8 text to the system clipboard; empty data clears it |

OSC52 writes are bounded to 1 MiB and coalesced; replay does not touch the
clipboard. Clipboard read queries receive an empty reply. Window/screen reports
use the GUI owning the pane, not guessed server-side dimensions. Unsupported
terminal capabilities return a negative report. Responses are bounded and replay
suppresses them. BEL and ST terminators and output split across chunks are tested.
Run `~/.venv/bin/python scripts/go-ui-query-smoke.py` on macOS for native checks:
it compares 18 actual PTY replies with GUI metrics before and after a split,
checks OSC7 and OSC52, and restores the previous clipboard text. Only its own
temporary GUI/server are closed.

The macOS background smoke runs a continuous ANSI output command in an isolated
dev GUI and switches to Google Chrome for 2, 5, 10, 20 and 40 seconds. It confirms
that Water loses focus and macOS reports complete occlusion, then returns through
the real Show Water action and Launch Services. A private, unsigned test bundle
lets Launch Services identify the owned GUI; it is never installed. The test
checks output sequence progress, actual screenshot rendering, keyboard response
and a shell input/output round-trip. Requests exceeding 500 ms trigger thread
sampling. GUI/server PIDs, binary identity, latency records and screenshots go
under `/tmp/water-background.*`; only owned Water processes are closed.

Use `--external-app` to select a different installed application that covers the
test window, `--background-steps 2,5,10,20,40` to set increasing intervals, and
`--config-template PATH` to copy terminal and appearance settings into the
isolated configuration. Workloads include `plain`, `ansi`, `unicode`,
`hyperlinks` and `idle`; `--rate-mib` controls the output rate. Use
`--background-steps 80,180` for longer background periods; the output command's
lifetime grows with the requested intervals. `--background-mode hide` and
`minimize` separately exercise those native actions. This extended
reproduction intentionally exceeds the usual one-minute test limit. A slow
control reply or screenshot does not by itself prove a GUI hang: CLI/transport
and PNG encoding contribute to these timings, and a thread sample identifies
where the delay occurs. The test does not run `brew upgrade` or change installed
packages.

The settings smoke verifies navigation, editing, Ctrl/Alt combinations and function-key bytes in a real raw PTY, Ctrl+C process interruption, titlebar-integrated tabs, undecorated native-window state, titlebar drag and double-click, maximize/restore, border resize, minimize and close, together with settings and pane operations. Regression tests classify every physical key, check keypad and modified-key encoding, and verify font metadata/style selection and collection face indices. The UI uses one compact 36-dp toolbar with tabs and window controls, without a bottom status bar; macOS uses the system interface font by default. Font lookup reads internal family/subfamily names instead of guessing from filenames. `ui snapshot` reports `terminal_font` and `terminal_font_styles` with the actual file, style, collection index and fallback/loading status. `ui screenshot` captures Ebitengine's rendered frame, including the custom titlebar. Native clipboard and IME candidate-window acceptance still use the manual checks in `scripts/run-go-ime-manual.sh`; control-injected text does not substitute for OS input-method validation.

Terminal and UI font fields accept comma-separated family names, for example `Sarasa Term SC Nerd Font, Apple Color Emoji`. Fonts are tried in the specified order for each glyph; unavailable families are skipped. This also works in `terminal.font_family` and `ui.font_family` in the JSON config. Terminal block elements fill their cell fractions directly so adjacent rows join regardless of line height. Emoji use installed color-font fallback and wide terminal cells.

The Go renderer draws Box Drawing (U+2500–257F), Block Elements (U+2580–259F), Braille Patterns (U+2800–28FF), and sextant blocks (U+1FB00–1FB3B) directly on the physical cell grid. This includes mixed line weights, double lines, dashed lines, rounded corners and diagonals. These ranges do not depend on the selected font's ink bounds or ligatures; other Unicode symbols and ASCII art use the configured font fallback chain.

On macOS, the application menu follows the configured `hide_window` and `minimize_window` shortcuts. Defaults are Cmd+W to hide, Cmd+M to minimize, Cmd+Q to ignore quitting, and Cmd+H to focus the left pane. **Quit GUI** follows the existing `detach_on_quit` policy; **Quit GUI and Local Server** explicitly shuts down the attached local server before closing the GUI. Remote servers retain their own lifecycle. The lifecycle smoke invokes actual native menu items and verifies that the owned server PID and socket disappear. `water ctl ui menu quit-and-server` invokes the same menu item; `ui menu show-window` restores a hidden or minimized window. `ui snapshot` includes native menu state and shutdown errors. Terminal row height and baseline account for ascent and descent across font styles, with configured line height as a minimum.

The Go settings panel covers the configuration schema, including sidebar, host/workspace/agent geometry, agent colors, dimming, tab-wheel behavior, cross-host workspace navigation and window shortcuts. Each control indicates whether it applies immediately, to new windows or after restarting. Existing override documents and unknown keys survive saving. Cmd+N starts a separate native GUI process attached to the current server, loading the saved window preferences. Workspace and tab rename dialogs use the normal command dispatcher; sidebar Agent rows activate their owning connection, tab and pane.

Terminal settings include `cursor_style` (`block`, `bar`, `underline`) and
`cursor_blink` (`true` or `false`). Both apply immediately; the defaults remain
a blinking block. Applications can override these preferences with DECSCUSR;
CSI 0 SP q restores the configured defaults.

Agent rows use foreground-process detection and, when the program emits it, client-side OSC `9;4;<state>[;<percentage>]` progress metadata. States `1`/`3` show Running, `0` shows Idle, `2` shows Error, and `4` shows Paused. Paused can also mean a warning; it does not prove that approval is required. Programs without progress reports retain the process-based Running/Exited status. No Agent hooks are installed. With `ui.system_notifications` enabled, Agent starts, completion, attention and error transitions send distinct notifications; initial snapshots and reconnect recovery are baselined. `ui.agent_long_run_notification_seconds` (default 600, 0 disables) controls background Agent alerts. `ui.shell_long_run_notification_seconds` (default 300, 0 disables) controls alerts for foreground shell commands while both the Water window and command pane are unfocused; interactive shells are ignored. Progress reports must reach Water directly; programs that gate reporting on terminal identity, or multiplexers that filter OSC, may not emit them to Water.

Rounded windows use Ebitengine screen transparency, a cached corner shader and macOS content-layer clipping. `ui.window_corner_radius` applies immediately; 0 gives square corners. Maximized/fullscreen windows use square corners and restore the configured radius afterwards. The parity smoke checks actual native layer state and screenshot corner alpha, edits migrated settings through their real controls, and exercises rename, sidebar resizing/visibility, and Agent focus. Schema parity and round-trip tests catch fields silently omitted from the migration.

## Terminal images, history and paste

Text selection remains active while output arrives. History clicks remain local even
when the foreground application enables mouse tracking. Selected lines follow
history trimming; an expired selection is cleared rather than copying another line.

Terminal screen clears use a common client-side policy. Shell `CSI 2 J` moves
cleared content into bounded history. Agents that redraw their transcript can
instead replace their own history while retaining the prefix preceding their
first synchronized output frame. Pi selects this policy through the Agent
registry; its `CSI 3 J` removes previous Pi renderings, so Ctrl+O expansion and
collapse do not accumulate duplicate welcome or tool output. The boundary
follows history trimming and reflow and is rebuilt during attach replay.

Run `~/.venv/bin/python scripts/go-ui-pi-redraw-smoke.py` to verify the installed
Pi in regular mode against a local saved tool-output fixture, without submitting
a model request. It checks two Ctrl+O cycles across a viewport, retained shell
history, a single welcome, and keyboard/Enter following the bottom. It saves
screenshots and reads the GUI's actual history through the bounded, read-only
`ui content` interface, without selecting text or touching the clipboard.
`scripts/go-ui-progress-smoke.py` also measures the rendered status-icon bounds
for Running, Paused, Error and Idle in real GUI screenshots.

Water displays static Kitty RGB/RGBA/PNG streams (including compressed chunks and
Unicode placeholders), iTerm2 inline images and Sixel. Images follow their buffer's
history, retain their cell height when partially visible, and disappear when their
last covered row expires. Kitty screen deletion preserves history; `kitten icat
--clear-all` also removes historical images. `kitten icat` negotiates stream transfer;
shared-memory and temporary-file transfer are not advertised. Advanced Kitty
animation and relative placements are not implemented.

On macOS, the configured paste shortcut recognizes native PNG, TIFF and other image
clipboard types and sends Ctrl+V to the foreground agent, preserving the clipboard
for its own image reader. This is verified with Codex and Claude Code. Text uses
the usual bracketed-paste path. Remote agents need their own clipboard integration;
the local clipboard is not uploaded automatically.

Zsh startup installs a one-shot forward-delete binding only when the key is
unbound, without changing user startup files. Explicit `-f` and command-mode
shells retain their isolated startup behavior.

```sh
go build -o target/go-ui-smoke/water ./cmd/water
~/.venv/bin/python scripts/go-ui-graphics-history-smoke.py
WATER_TEST_AGENT='codex --no-alt-screen' \
  ~/.venv/bin/python scripts/go-ui-graphics-history-smoke.py
WATER_TEST_AGENT=claude ~/.venv/bin/python scripts/go-ui-graphics-history-smoke.py
```

The macOS smoke preserves the native clipboard, creates an owned GUI/server with
a unique socket/config, and saves screenshots. Set `WATER_TEST_IMAGE` to override
the sample JPEG path. Agent verification attaches the image without submitting a
model request. Kitty protocol details are defined in its [graphics specification](https://sw.kovidgoyal.net/kitty/graphics-protocol/).

## Startup options

The GUI applies a soft Go memory budget of at least 192 MiB, increasing it when
live data needs 64 MiB of allocation headroom. An explicit `GOMEMLIMIT` overrides
this policy. GPU textures and native allocations are outside this budget; it is
not a process memory ceiling. Fonts retain compact outlines with a bounded lazy
cache, and bitmap emoji load the strike appropriate to the requested size.
The local typesetting patch and its upgrade/test instructions are documented in
`third_party/typesetting/WATER_CHANGES.md`.

For isolated continuous-output memory and input measurements:

```sh
go build -tags water_cpu_diagnostic -o /tmp/water-profile ./cmd/water
~/.venv/bin/python scripts/diagnose-gui-cpu.py --water /tmp/water-profile \
  --memory --sustained-seconds 40 --config /path/to/config.json
~/.venv/bin/python scripts/diagnose-gui-input.py --water /tmp/water-profile \
  --timings --seconds 25 --rate-mib 4 --config /path/to/config.json
```

These scripts create owned GUI instances with unique sockets and temporary
configuration, and close only those instances. `--memory` records memory without
forcing GC. Input reports distinguish control-command round trips from bounded
native update/draw timing counters; neither replaces manual OS keyboard/IME
acceptance. The diagnostic HTTP endpoints exist only in the tagged build.
`scripts/diagnose-gui-cadence.py --water /tmp/water-profile` exercises paced
output and 50 Hz GUI text input through the same framed control transport,
reporting changed-frame intervals separately from work duration. Rendering uses
immutable changed-row views: the first output is published immediately, and
continuous output is coalesced on display consumption at up to 30 publications
per second. Input activity follows display sync independently; idle windows
retain the last frame. This does not replace physical keyboard/IME verification.
The latest [CPU pipeline report](cpu-pipeline-profile-2026-10-04.md) records stage
counters, CPU and allocation profiles, throughput limits, and the presentation
cadence tradeoff.
See [the memory and input profiling report](memory-input-profile-2026-10-04.md)
for the measured results and remaining limits.

The [server memory and cursor report](server-memory-cursor-ime-2026-10-05.md)
records the idle PTY allocation fix and native Retina cursor/IME checks.
`water ctl debug memory` reports Go heap allocated, in-use, idle and released
bytes separately from retained PTY replay. `terminal_count` counts active PTYs;
`exited_terminal_count` and `exited_replay_bytes` report the recent-exit cache.
Natural exits retain complete replay for `terminal.wait_exit` and
`terminal.snapshot`, up to 32 terminals and 8 MiB total replay accounting. The
oldest exits are evicted when either limit is exceeded; queries for evicted IDs
return terminal-not-found. `retained_replay_bytes` includes active and cached
replay. Explicit closure removes the terminal instead of caching it.
Queue capacity does not reserve an
8 MiB read pool per idle PTY: reader blocks are allocated on demand, with at
most two spare 128 KiB blocks cached after bursts.
Run `WATER_BIN=/path/to/dev/water ~/.venv/bin/python scripts/go-ui-cursor-smoke.py`
to verify all six cursor preferences, persistence and IME caret geometry in an
isolated native GUI. Physical input-method candidate-window acceptance remains
a separate check.

On macOS, `~/.venv/bin/python scripts/go-ui-detached-startup-smoke.py`
verifies the packaged dev app's cold start through `open`, with no existing
server and an inherited legacy `WATER_GO_DAEMON_CHILD=1` marker. It also checks
standalone daemon startup and that the marker is absent from the new PTY shell.
The script uses an owned temporary configuration/socket and closes its test
GUI/server; it does not restart an existing user instance.

For intermittent macOS beachballs when returning to a background window, monitor
the existing GUI through its control API and capture its stacks while a request
is slow:

```sh
~/.venv/bin/python scripts/diagnose-gui-stalls.py \
  --water /Applications/Water.app/Contents/MacOS/water --duration 60
```

Use the running dev bundle's `water-dev` executable for dev, and `--socket` for
an explicit control socket. The monitor requires exactly one GUI at the supplied
executable path. It sends only `ui state`, never terminal input or window actions,
and samples that GUI if a probe exceeds 500 ms. The private temporary report
directory contains latency records, build/server identity and any macOS thread
samples; terminal content is not requested. CLI startup and control transport
contribute to the measured latency, so a slow probe alone does not prove a GUI
hang. A run without a slow probe does not rule out intermittent stalls.

```sh
# Default dev GUI.
target/go-ui-smoke/water

# Start with no model objects, useful for a clean control/scenario baseline.
target/go-ui-smoke/water --empty-workspace

# Keep a workspace but do not create its initial terminal.
target/go-ui-smoke/water --no-initial-terminal

# Select a config and socket for an isolated instance.
target/go-ui-smoke/water \
  --config /tmp/water-docs/config.json \
  --control-socket /tmp/water-docs.sock

# Attach the GUI to a remote Water server through SSH.
target/go-ui-smoke/water --ssh build-box
```

The default shell is the configured real shell; on macOS, zsh resolution prefers `/opt/homebrew/bin/zsh`. New tabs and split panes create a shell terminal. `WATER_CONFIG=/path/to/config.json` is equivalent to `--config`.

The GUI and server are separate when `server.detached` is enabled. `--empty-workspace` and `--no-initial-terminal` are test entry points, not alternate product modes.

Settings → **Server** shows bilateral capabilities for the selected Local or Remote connection. Product-version differences are allowed when the API remains compatible; a server source-revision change prompts an explicit restart. Save layout and confirmed restart retain layout/selection/current directories and create new shells. Running jobs and terminal history are not resumed. Legacy servers without advertised recovery support remain running. See [Server compatibility and recovery](server-compatibility.md) for admission, stable remote discovery, failure handling and artifact locations.

## `water ctl`

The control client is built into the `water` binary; the old standalone `waterctl` binary no longer exists. Prefer the explicit `ctl` namespace:

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ping
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock info
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock server info
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock connections list
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock state
```

The socket resolves from `--socket`, `WATER_CONTROL_SOCKET`, `server.socket_path`, `startup.control_socket`, and then the dev/release default. Bare aliases such as `water state` and `water ui key cmd-t` remain available, but `water ctl` makes scripts unambiguous.

### Model and terminal commands

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock workspace new
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock tab new
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock pane split --right
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock pane content \
  --pane PANE_ID --row 0 --rows 8 --column 0 --columns 120
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock terminal snapshot \
  --terminal TERMINAL_ID
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock debug memory
```

Mutating commands return an operation snapshot. Synchronize on the returned operation, terminal output, revision, or process exit; do not add arbitrary sleeps to make a test pass.

### Real GUI control

`ui key` dispatches a keystroke through the GUI's input handler. `ui click` dispatches a control-API click at a window-local point, `ui wheel` sends a wheel event, `ui state` reports the current window state, and `ui screenshot` captures the real Ebitengine rendered frame.

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui key cmd-t
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui click \
  --x 240 --y 100 --click-count 2
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui state
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock ui screenshot \
  --output /tmp/water-window.png
```

Use Water's control API for GUI checks. Do not use `xdotool`, `xte`, AppleScript keystrokes, or native desktop coordinate injection. The control screenshot is a Water-window capture, not a desktop-wide screenshot; platform support must be verified on the machine running the check.

### Scenarios

```sh
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/workspace_basic.json
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/terminal_basic.json
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/terminal_zsh.json
target/go-ui-smoke/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/agent_detection.json
```

Scenarios are expected to use bounded operation/output/exit waits. Run them against a dedicated socket and temporary config, then shut down only the server created for that run.

## Configuration

On macOS the native default is:

```text
~/Library/Application Support/water/config.json
```

The dev variant uses the `water-dev` directory; release uses `water`. An existing XDG-style `~/.config/water/config.json` may also be selected. `--config`/`WATER_CONFIG` selects an explicit file. Missing files use built-in defaults; malformed files fail startup rather than being silently ignored.

[`config.example.json`](../config.example.json) contains the full current schema. It covers startup/window sizing, server lifecycle, shell and arguments, terminal dimensions and bounded history, theme/UI/sidebar metrics, features, agent colors, and shortcuts. The Settings page reads and writes the same schema. UI/theme/font/feature/shortcut changes can apply to existing windows; shell, startup, terminal history, and default terminal size changes require restart.

## Build

```sh
# Dev identity; ordinary Go builds default to dev.
mkdir -p target/go-ui-smoke
go build -o target/go-ui-smoke/water ./cmd/water
go build -o target/go-ui-smoke/water-server ./cmd/water-server

# Assemble the macOS app; dev is the default variant.
bash scripts/build-go-macos-app.sh

# Assemble a release app explicitly.
WATER_APP_VARIANT=release bash scripts/build-go-macos-app.sh

# Native Linux package, on a Linux host.
WATER_APP_VARIANT=release bash scripts/build-go-linux.sh

# Refresh embedded server payloads only.
WATER_APP_VARIANT=dev bash scripts/build-go-embedded-servers.sh
```

The packaging scripts build the GUI, dedicated server and four embedded server targets. GUI builds are native to the target desktop platform; headless server payloads use Go cross-compilation. The app bundle must contain payloads whose variant and version match the GUI. Product version comes from the root `VERSION` file; use the same `WATER_APP_VERSION` for packaging and publishing when overriding it. `WATER_APP_VARIANT` selects dev/release and is injected through linker flags. macOS build directories are `target/go-app/<variant>`; Linux uses `target/go-linux/<variant>`; archives go to `dist`.

## Tests and validation

The reusable execution procedure is [testing-strategy.md](testing-strategy.md).
Use `~/.venv/bin/python scripts/run-go-regression.py --profile full --list` to
inspect the plan, then run the matching `unit`, `terminal`, `agent`, or `full`
profile. Each step has a 60-second deadline; logs and a machine-readable summary
are retained under `target/test-runs`. New GUI tests reuse `scripts/water_test.py`.

```sh
gofmt -l cmd internal
go vet ./...
go test ./... -count=1 -timeout=60s
go test ./internal/goclient ./internal/goserver ./internal/goterminal -race -timeout=60s

# Build dedicated scenario binaries, then run the fixtures.
mkdir -p target/go-scenarios
go build -o target/go-scenarios/water ./cmd/water
go build -o target/go-scenarios/water-server ./cmd/water-server
bash scripts/run-go-scenario-suite.sh

# Headless performance and interaction checks.
go test ./internal/gobench -count=1 -timeout=60s
```

For a real GUI smoke run, start the current binary with a unique socket/config and exercise `ui key`, `pane input`, and `ui state` through `water ctl`. Ordinary shell output can use `pane content`, which reconstructs replay in the CLI. To inspect the actual GUI client viewport or history, use `water ctl ui content --pane UUID [--window UUID]`; `--start-row N --rows M` reads absolute retained rows without scrolling or selection (maximum 256 rows / 1 MiB of cell text). Check response identity and sequence/trim/buffer consistency across pages. Parsed content is distinct from a rendered frame, so use screenshots for drawing assertions. Record the GUI/server PID and clean it up after the run. Cross-build and headless tests do not replace a native GUI check.

Cmd+N windows share one local server/socket and can select different workspaces, tabs and panes. Closing a window releases only its session; with `detach_on_quit=false`, the last window closes the server. An embedded server keeps running in its original process after that process's GUI closes while other windows remain. Explicit **Quit GUI and Local Server** shuts down the shared local server using the existing connection, even if its socket pathname has become unavailable.

`water ctl server info` lists UUID `window_id` values and the current focus owner, plus build/revision/capability metadata. `water ctl ui state --window <UUID>` (also supported by other `ui` commands) targets a specific window; omitted IDs target the latest focused GUI. `terminal_grids.columns/rows` describe that window's geometry; `terminal_columns/terminal_rows` report the shared terminal's current dimensions. Only the focused window changes shared PTY dimensions; obtaining focus reapplies its pane geometry. Protocol/API or required-capability mismatches enter diagnostic mode instead of normal workspace operations. Existing sockets are never silently replaced.

Native multiwindow checks cover both detached and embedded lifetimes:

```sh
~/.venv/bin/python scripts/go-ui-multiwindow-smoke.py --binary target/go-ui-smoke/water
~/.venv/bin/python scripts/go-ui-multiwindow-smoke.py --binary target/go-ui-smoke/water --embedded
```

Builds and checks run locally. CI only retains `.github/workflows/macos-signed.yml`, which is manually dispatched and does no compilation.

## In-app updates (macOS and Linux)

Open **Settings → Software update**, or the macOS application menu's
**Software update** item. Water checks its public GitHub Releases, offers a
download, then **Install and restart**. Checking and downloading run outside
the rendering loop. No DMG mounting or dragging an app is required after
installing a version that includes this updater.

Only strictly newer product versions with an exact platform/architecture asset
are eligible. Release builds accept non-draft, non-prerelease `v*` releases;
dev builds use non-draft `dev-*` releases, including timestamp tags whose
product version is obtained from the package name. Same-version dev rebuilds
are not updates: increment `WATER_APP_VERSION` when publishing an update.
macOS uses `Water[- Dev]-VERSION-macOS-{arm64,amd64}.zip` (GitHub replaces spaces
with periods); Linux uses `water[-dev]-VERSION-{aarch64,x86_64}-linux.tar.gz`.
Publish Linux packages to the matching release after building them natively.
macOS development machines sign locally with the same certificate as CI; the
signing-only workflow remains a fallback for machines without local signing.

Both platforms require the SHA256 digest provided by GitHub's release-assets
API and verify the complete download before extracting it. macOS additionally
requires the new bundle to satisfy the current app's certificate designated signing
requirement, and verifies bundle identity, version and nested signatures.
Unsigned/ad-hoc macOS development bundles cannot install updates. Linux trusts
the official HTTPS release metadata and its digest; it does not claim an
independent publisher signature. The GUI, sibling server and private headless installer identities are
checked before installation. Invalid packages and archive links/path traversal
are rejected.

The installation directory must be writable. macOS must run from an installed
app bundle, outside a mounted DMG or App Translocation; Linux requires the GUI
and sibling server from the extracted package in the same directory. A system
package manager installation should continue using that package manager when
its directory is not writable. Water does not request elevated privileges.

An independent installer waits for the GUI to exit, retains the old app/binaries,
and rolls back if replacement fails or the new GUI fails to render a live frame
within 20 seconds. The control socket, config and SSH destinations are preserved.
Detached terminal servers continue running during the GUI restart, including
when `detach_on_quit` is false. An embedded local server blocks installation;
enable `server.detached` and restart Water before installing updates. Remote
connections reconnect through the existing transport. Protocol incompatibility
with a retained server causes startup failure and rollback rather than server
replacement or session termination.

Failed installations retain `.water-update-*` (release) or `.water-dev-update-*` (dev) staging/backup files and
`install.log` beside the installation. A crashed installer may leave
`.water-update-lock` / `.water-dev-update-lock`; remove that lock only after confirming no installer for
this installation is running. Normal successful installation cleans its files.
No update is installed without clicking **Install and restart**.

Run `~/.venv/bin/python scripts/go-ui-update-smoke.py` with `WATER_BIN` pointing
to the native test binary. It uses an isolated socket/config and real Water
control actions, checks the localized panel/menu and bounded release lookup,
and captures screenshots without installing a release.

## macOS local signing and CI fallback

The macOS app registers as an alternate handler for `.command` files. Choose
Water in Finder's **Open With** menu to run an executable command file in a new
local workspace. Its working directory is the script's directory; the configured
shell remains open after it finishes. Local files are rejected in `--ssh` sessions.

On a macOS development machine, build and sign locally with the same SurTeam
certificate used by CI. Supply `CODESIGN_IDENTITY` securely from the local
keychain/environment; do not put the literal identity or credentials in source,
logs or shell tracing. Do not dispatch signing CI after successful local signing.

```sh
# CODESIGN_IDENTITY must already be set to the matching CI certificate.
WATER_APP_VARIANT=release CODESIGN_REQUIRED=1 \
  bash scripts/build-go-macos-app.sh
codesign --verify --deep --strict dist/Water.app
APP_VERSION="${WATER_APP_VERSION:-$(cat VERSION)}"
case "$(uname -m)" in arm64) ARCH=arm64 ;; x86_64) ARCH=amd64 ;; esac
test -s "dist/Water-${APP_VERSION}-macOS-${ARCH}.zip"
unzip -tq "dist/Water-${APP_VERSION}-macOS-${ARCH}.zip"
```

The native script signs updater, server, GUI and app using the same runtime and
timestamp options as CI, then verifies the bundle. Commit/push the version and
tag before uploading the signed archive to the matching release. Local signing
failure is a blocker; never silently substitute ad-hoc signing or CI.

When local signing is unavailable, the existing two-stage CI handoff is retained:
the build machine creates an unsigned archive and the signing-only GitHub Action
downloads, signs, verifies and publishes it. It never checks out or compiles source.

```sh
# Release: push the vVERSION tag first.
APP_VERSION="${WATER_APP_VERSION:-$(cat VERSION)}"
WATER_APP_VARIANT=release CODESIGN_SKIP=1 \
  bash scripts/build-go-macos-app.sh
WATER_APP_VARIANT=release WATER_RELEASE_TAG="v${APP_VERSION}" \
  WATER_RELEASE_PUBLICATION=release bash scripts/publish-unsigned-macos.sh

# Dev: use an already pushed dev-* tag.
DEV_TAG="dev-$(date -u +%Y%m%d-%H%M)"
WATER_APP_VARIANT=dev CODESIGN_SKIP=1 \
  bash scripts/build-go-macos-app.sh
WATER_APP_VARIANT=dev WATER_RELEASE_TAG="$DEV_TAG" \
  WATER_RELEASE_PUBLICATION=prerelease bash scripts/publish-unsigned-macos.sh
```

`publish-unsigned-macos.sh` validates the archive with `test -s` and `unzip -t`, creates or updates the target tag's draft release, then dispatches `macos-signed.yml` with `variant`, `publication`, `tag`, `source_release`, and `source_asset`. Release tags must start with `v`; dev tags must start with `dev-`. The Action only signs, verifies, optionally notarizes, and publishes. Keep signing materials outside the repository and provide them only through the documented GitHub secrets. Local unsigned/ad-hoc bundles are intermediate artifacts; credentialed signing must use `CODESIGN_IDENTITY` and `CODESIGN_REQUIRED=1`. `CODESIGN_SKIP=1` and `CODESIGN_REQUIRED` are mutually exclusive.
