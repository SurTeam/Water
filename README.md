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

The app starts a local control socket at `/tmp/water.sock` by default, uses `~` as the default terminal cwd, and opens one Terminal tab connected to the detected real zsh (preferring `/opt/homebrew/bin/zsh`) with `-l` (login mode). New tabs and split panes also create a real-shell terminal automatically. In the GUI, `Cmd-N` opens another native Water window, `Cmd-W` hides the active window without terminating Water or its workspace state, `Cmd-M` minimizes it, and `Cmd-Q` is intentionally ignored. `Cmd-\\` splits right (horizontal left/right layout), `Cmd--` splits down (vertical up/down layout), and `Cmd-T` creates a new terminal tab. `Cmd-,` opens the standalone Settings page; it is also available from the Water menu. The same commands are available from the macOS menu bar. Use `--no-initial-terminal` to keep the workspace but omit the initial tab/PTY, or `--empty-workspace` for a completely clean model baseline. Override the socket with:

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

Configurable defaults are kept outside the model in a JSON file and loaded at startup. On macOS the native default path is:

```text
~/Library/Application Support/water/config.json
```

The conventional XDG path `~/.config/water/config.json` (or `$XDG_CONFIG_HOME/water/config.json`) is also supported and is selected when it already exists. `WATER_CONFIG=/path/to/config.json` or `--config /path/to/config.json` selects another file. The file is an override layer: omitted fields keep Water's built-in defaults (both the flat schema and an optional `{ "overrides": { ... } }` wrapper are accepted), while the Settings page writes a complete, readable JSON document. [`config.example.json`](config.example.json) contains the complete schema.

Open Settings with `Cmd-,` or the Water menu. It exposes startup defaults (cwd, control socket, initial workspace/terminal, window size and window minimum width/height), shell program and ordered arguments, terminal dimensions/scrollback/font metrics, terminal features, UI base font size, every current theme color, sidebar/layout metrics, and all current Water shortcuts. Sidebar settings include the panel background (`theme.sidebar_background`), unselected workspace and agent row backgrounds (`theme.sidebar_workspace_background` and `theme.sidebar_agent_background`), selected workspace and focused agent row backgrounds (`theme.sidebar_workspace_active_background` and `theme.sidebar_agent_active_background`, both defaulting to the green accent), per-agent accent colors, the optional running-agent count badge (`ui.sidebar_show_agent_count`), and an opt-in vertical-wheel scroll for the tab strip (`ui.tab_bar_vertical_wheel_scroll`, default off; trackpad horizontal swipes always scroll it; overflow arrows mark hidden content and the `+` button rides after the last tab). `theme.agent_colors` uses the keys `claude_code`, `codex`, `opencode`, `gemini_cli`, `aider`, `cursor_agent`, `amp`, `crush`, `goose`, `qwen_code`, `droid`, `grok`, and `pi`. The tab shortcuts include a `switch_tab` template (`cmd-#`, where `#` becomes 1–9 or 0 for tabs 1–10), `next_tab`/`previous_tab`, and cyclic workspace navigation with `next_workspace`/`previous_workspace`. Each row declares whether it takes effect immediately, for a new window, or after restarting Water. UI/theme/font/feature/shortcut changes are applied to existing workspace windows after a successful save; shell, startup, terminal history, and default-terminal-size changes are marked for restart so existing PTY workers are never silently reconfigured. The Settings page has a Restart Water button; when there are unsaved changes it saves them first and only restarts after persistence succeeds. The built-in dark theme mirrors the Kitty `kitty_normal.conf` base colors (`#2c2c2c` background and `#e4e4e4` foreground) and uses Kitty green `#339966` for the active pane/tab UI accent (`theme.active_pane_border` and `theme.tab_active_background`). These remain Water-owned AppConfig defaults; Kitty is not read at runtime. When the focused terminal is scrolled away from live output, it borrows unused aggregate byte budget to keep the visible rows pinned; returning to the live end or typing releases that temporary capacity. A terminal that loses focus is trimmed to `terminal.inactive_scrollback_lines` (default 500 rows) and its scrollback reservation is released back to the shared `terminal.max_total_scrollback_bytes` budget, so opening many long tabs keeps memory bounded. When the system has no `alacritty` terminfo entry, Water's shells use the bundled `assets/terminfo` database (override location with `WATER_TERMINFO_DIR`), and Water also injects a lightweight zsh integration so the `clear` command additionally erases scrollback while Ctrl-L keeps its classic "push the prompt to the top, keep history" behavior (opt out with `WATER_NO_SHELL_INTEGRATION=1`). `waterctl debug memory` reports the live scrollback/registry byte accounting. Missing files use built-in defaults; malformed files fail startup instead of being silently ignored.

```sh
mkdir -p "$HOME/Library/Application Support/water"
cp config.example.json "$HOME/Library/Application Support/water/config.json"
# The checked-in example uses the current built-in defaults, including ~/,
# a 16px terminal font, 14px UI text, and a 170px sidebar.
# For example, edit terminal.scrollback_lines, terminal.inactive_scrollback_lines,
# terminal.max_total_scrollback_bytes,
# or theme.inverse_background.
```

Windows resize freely down to `startup.window_min_width`/`window_min_height` (new windows take new values; the Water menu has a no-shortcut Quit item; cmd-q stays inert by design).

## Control

```sh
cargo run --bin waterctl -- --socket /tmp/water.sock state
# Dispatch a real GPUI keystroke through the focused native window/action tree.
cargo run --bin waterctl -- --socket /tmp/water.sock ui key cmd-t
cargo run --bin waterctl -- --socket /tmp/water.sock ui state
# Capture only the active Water window (not the full desktop).
cargo run --bin waterctl -- --socket /tmp/water.sock ui screenshot --output /tmp/water.png
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

`ui key` is a running-application test interface: the control thread hands the keystroke to the GPUI thread, which calls `Window::dispatch_keystroke` against the active/frontmost Water window. It therefore exercises the actual keymap and focused element action handlers instead of directly invoking model commands. `ui screenshot` asks GPUI to render that same active Water window to a PNG; it does not invoke the system full-screen screenshot tool. `pane input` targets a pane through `TerminalCommand::SendText`, and `pane content` returns the requested viewport row/column range.

The scenario runner waits on operation completion, terminal output, and process exit primitives. It does not use fixed sleeps, coordinates, or screenshots. When a terminal process exits, Water automatically closes its pane; if it was the tab's final pane, the tab is closed as well. The completed terminal snapshot remains available through bounded wait/query state so exit observers are not raced by the UI cleanup.
