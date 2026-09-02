# water

`water` is a native Rust desktop workbench built around a programmable application model. Phase 1 established the workspace, tab, pane-tree, command, operation, event, control-socket, and scenario foundations. Phase 2 adds an `alacritty_terminal`-backed terminal surface with a dedicated PTY worker. Phase 3 currently focuses on making that terminal genuinely interactive: focus, keyboard input, navigation, paste, scrolling, selection/copy, resize reporting, and rendering quality.

## Requirements

- Rust stable with Rust 2024 edition support
- macOS is the first supported platform
- GPUI is pinned to the upstream Zed revision documented in [`ARCHITECTURE.md`](ARCHITECTURE.md)
- macOS Xcode must include the Metal Toolchain (`xcodebuild -downloadComponent MetalToolchain`)

## Run

```sh
cargo run --bin water
```

The app starts a local control socket at `/tmp/water.sock` by default and opens one Terminal tab connected to the detected real zsh (preferring `/opt/homebrew/bin/zsh`) with `-l` (login mode). New tabs and split panes also create a real-shell terminal automatically. In the GUI, `Cmd-N` opens another native Water window, `Cmd-W` hides the active window without terminating Water or its workspace state, `Cmd-M` minimizes it, and `Cmd-Q` is intentionally ignored. `Cmd-\\` splits right (horizontal left/right layout), `Cmd--` splits down (vertical up/down layout), and `Cmd-T` creates a new terminal tab. The same commands are available from the macOS menu bar. Use `--no-initial-terminal` to keep the workspace but omit the initial tab/PTY, or `--empty-workspace` for a completely clean model baseline. Override the socket with:

```sh
cargo run --bin water -- --control-socket /tmp/my-water.sock
```

## Build a macOS `.app`

On macOS, build the release binary, assemble `dist/Water.app`, install the Water icon, validate the plist, and ad-hoc sign the bundle with:

```sh
./scripts/build-macos-app.sh
open dist/Water.app
```

The editable icon source is [`assets/macos/Water.svg`](assets/macos/Water.svg); the generated bundle resource is [`assets/macos/Water.icns`](assets/macos/Water.icns). For distribution signing, set `CODESIGN_IDENTITY` to a valid Developer ID Application identity before running the script. Notarization remains a separate release step.

## Application defaults

Configurable defaults are kept outside the model in a JSON file and loaded at startup. On macOS the default path is:

```text
~/Library/Application Support/water/config.json
```

`WATER_CONFIG=/path/to/config.json` or `--config /path/to/config.json` selects another file. [`config.example.json`](config.example.json) contains the complete schema. The current defaults include terminal features (`selection`, `mouse_reporting`, and `bracketed_paste`), theme colors, terminal font metrics, the per-terminal `scrollback_lines` limit (default `10000`), and the aggregate temporary scrollback cap `max_total_scrollback_lines` (default `100000`). The built-in dark theme mirrors the Kitty `kitty_normal.conf` base colors (`#2c2c2c` background and `#e4e4e4` foreground) and uses Kitty green `#339966` for the active pane/tab UI accent (`theme.active_pane_border` and `theme.tab_active_background`). These remain Water-owned AppConfig defaults; Kitty is not read at runtime. When the user scrolls away from live output, the terminal borrows unused aggregate capacity to keep the visible rows pinned; returning to the live end or typing releases that temporary capacity. Missing files use built-in defaults; malformed files fail startup instead of being silently ignored.

```sh
mkdir -p "$HOME/Library/Application Support/water"
cp config.example.json "$HOME/Library/Application Support/water/config.json"
# For example, edit terminal.scrollback_lines, terminal.max_total_scrollback_lines,
# or theme.inverse_background.
```

## Control

```sh
cargo run --bin waterctl -- --socket /tmp/water.sock state
# Dispatch a real GPUI keystroke through the focused native window/action tree.
cargo run --bin waterctl -- --socket /tmp/water.sock ui key cmd-t
cargo run --bin waterctl -- --socket /tmp/water.sock ui state
cargo run --bin waterctl -- --socket /tmp/water.sock tab new       # creates a terminal tab
cargo run --bin waterctl -- --socket /tmp/water.sock pane split --right  # creates a terminal pane
# Pane-targeted input still mutates through AppCommand -> CommandDispatcher.
cargo run --bin waterctl -- --socket /tmp/water.sock pane input --pane PANE_ID --text $'printf "__PANE__\\n"\n'
# Query an exact viewport cell range without dumping the entire terminal.
cargo run --bin waterctl -- --socket /tmp/water.sock pane content --pane PANE_ID --row 0 --rows 4 --column 0 --columns 80
cargo run --bin waterctl -- --socket /tmp/water.sock debug memory
cargo run --bin waterctl -- --socket /tmp/water.sock terminal spawn /bin/sh -c 'printf "__READY__\\n"'
# Use the returned terminal ID for direct inspection or waits.
cargo run --bin waterctl -- --socket /tmp/water.sock terminal snapshot --terminal TERMINAL_ID
# In the GUI, click-drag selects terminal text; Cmd-C copies a selection, otherwise it sends Ctrl-C.
```

## Validation

```sh
cargo fmt --all -- --check
cargo clippy --all-targets -- -D warnings
cargo test --all-targets
./scripts/build-macos-app.sh
# Real-app smoke setup: start the bundled executable on a dedicated socket, then
# drive Cmd-N/W/M/Q, terminal shortcuts, pane input, and pane range queries via waterctl.
dist/Water.app/Contents/MacOS/water --control-socket /tmp/water-e2e.sock
cargo run --bin waterctl -- --socket /tmp/water-e2e.sock ui key cmd-n
cargo run --bin waterctl -- --socket /tmp/water-e2e.sock ui key cmd-m
cargo run --bin waterctl -- --socket /tmp/water-e2e.sock ui key cmd-w
cargo run --bin waterctl -- --socket /tmp/water-e2e.sock ui key cmd-q
# Start this in a separate terminal with --empty-workspace for the scenario baseline.
cargo run --bin water -- --control-socket /tmp/water.sock --empty-workspace
cargo run --bin waterctl -- --socket /tmp/water.sock scenario run tests/scenarios/workspace_basic.json
# terminal_zsh validates explicit Homebrew zsh; terminal_basic remains the portable shell fixture.
cargo run --bin waterctl -- --socket /tmp/water.sock scenario run tests/scenarios/terminal_basic.json
cargo run --bin waterctl -- --socket /tmp/water.sock scenario run tests/scenarios/terminal_zsh.json
```

`ui key` is a running-application test interface: the control thread hands the keystroke to the GPUI thread, which calls `Window::dispatch_keystroke` against the active/frontmost Water window. It therefore exercises the actual keymap and focused element action handlers instead of directly invoking model commands. `pane input` targets a pane through `TerminalCommand::SendText`, and `pane content` returns the requested viewport row/column range.

The scenario runner waits on operation completion, terminal output, and process exit primitives. It does not use fixed sleeps, coordinates, or screenshots. When a terminal process exits, Water automatically closes its pane; if it was the tab's final pane, the tab is closed as well. The completed terminal snapshot remains available through bounded wait/query state so exit observers are not raced by the UI cleanup.
