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

The app starts a local control socket at `/tmp/water.sock` by default and opens one Terminal tab connected to the detected real zsh (preferring `/opt/homebrew/bin/zsh`) with `-f`. New tabs and split panes also create a real-shell terminal automatically. In the GUI, `Cmd-\\` splits right (horizontal left/right layout), `Cmd--` splits down (vertical up/down layout), and `Cmd-T` creates a new terminal tab. Use `--no-initial-terminal` to keep the workspace but omit the initial tab/PTY, or `--empty-workspace` for a completely clean model baseline. Override the socket with:

```sh
cargo run --bin water -- --control-socket /tmp/my-water.sock
```

## Application defaults

Configurable defaults are kept outside the model in a JSON file and loaded at startup. On macOS the default path is:

```text
~/Library/Application Support/water/config.json
```

`WATER_CONFIG=/path/to/config.json` or `--config /path/to/config.json` selects another file. [`config.example.json`](config.example.json) contains the complete schema. The current defaults include terminal features (`selection`, `mouse_reporting`, and `bracketed_paste`), theme colors, terminal font metrics, and the per-terminal `scrollback_lines` limit (default `10000`). Missing files use built-in defaults; malformed files fail startup instead of being silently ignored.

```sh
mkdir -p "$HOME/Library/Application Support/water"
cp config.example.json "$HOME/Library/Application Support/water/config.json"
# For example, edit terminal.scrollback_lines or theme.inverse_background.
```

## Control

```sh
cargo run --bin waterctl -- --socket /tmp/water.sock state
cargo run --bin waterctl -- --socket /tmp/water.sock tab new       # creates a terminal tab
cargo run --bin waterctl -- --socket /tmp/water.sock pane split --right  # creates a terminal pane
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
# Start this in a separate terminal with --empty-workspace for the scenario baseline.
cargo run --bin water -- --control-socket /tmp/water.sock --empty-workspace
cargo run --bin waterctl -- --socket /tmp/water.sock scenario run tests/scenarios/workspace_basic.json
# terminal_zsh validates explicit Homebrew zsh; terminal_basic remains the portable shell fixture.
cargo run --bin waterctl -- --socket /tmp/water.sock scenario run tests/scenarios/terminal_basic.json
cargo run --bin waterctl -- --socket /tmp/water.sock scenario run tests/scenarios/terminal_zsh.json
```

The scenario runner waits on operation completion, terminal output, and process exit primitives. It does not use fixed sleeps, coordinates, or screenshots.
