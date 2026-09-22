# Water documents

This is the operational reference for contributors and users who need commands. The product overview is in [README.md](../README.md); implementation boundaries are in [ARCHITECTURE.md](../ARCHITECTURE.md); non-negotiable editing and release rules are in [AGENTS.md](../AGENTS.md).

## Requirements

Use stable Rust with Rust 2024 support. macOS builds need Xcode's Metal Toolchain. macOS app packaging also needs `rustup`, Zig, `cargo-zigbuild`, `gzip`, and `rg` for the embedded server targets. Linux GUI checks require an available display and the matching GPUI platform feature; do not assume that a Linux host has no X11/GPU.

## Run the GUI

```sh
# Default dev GUI; normal cargo build uses the dev identity.
cargo run --bin water

# Start with no model objects, useful for a clean control/scenario baseline.
cargo run --bin water -- --empty-workspace

# Keep a workspace but do not create its initial terminal.
cargo run --bin water -- --no-initial-terminal

# Select a config and socket for an isolated instance.
cargo run --bin water -- \
  --config /tmp/water-docs/config.json \
  --control-socket /tmp/water-docs.sock

# Attach the GUI to a remote Water server through SSH.
cargo run --bin water -- --ssh build-box
```

The default shell is the configured real shell; on macOS, zsh resolution prefers `/opt/homebrew/bin/zsh`. New tabs and split panes create a shell terminal. `WATER_CONFIG=/path/to/config.json` is equivalent to `--config`.

The GUI and server are separate when `server.detached` is enabled. `--empty-workspace` and `--no-initial-terminal` are test entry points, not alternate product modes.

## `water ctl`

The control client is built into the `water` binary; the old standalone `waterctl` binary no longer exists. Prefer the explicit `ctl` namespace:

```sh
target/debug/water ctl --socket /tmp/water-dev.sock ping
target/debug/water ctl --socket /tmp/water-dev.sock info
target/debug/water ctl --socket /tmp/water-dev.sock server info
target/debug/water ctl --socket /tmp/water-dev.sock connections list
target/debug/water ctl --socket /tmp/water-dev.sock state
```

The socket resolves from `--socket`, `WATER_CONTROL_SOCKET`, `server.socket_path`, `startup.control_socket`, and then the dev/release default. Bare aliases such as `water state` and `water ui key cmd-t` remain available, but `water ctl` makes scripts unambiguous.

### Model and terminal commands

```sh
target/debug/water ctl --socket /tmp/water-dev.sock workspace new
target/debug/water ctl --socket /tmp/water-dev.sock tab new
target/debug/water ctl --socket /tmp/water-dev.sock pane split --right
target/debug/water ctl --socket /tmp/water-dev.sock pane content \
  --pane PANE_ID --row 0 --rows 8 --column 0 --columns 120
target/debug/water ctl --socket /tmp/water-dev.sock terminal snapshot \
  --terminal TERMINAL_ID
target/debug/water ctl --socket /tmp/water-dev.sock debug memory
```

Mutating commands return an operation snapshot. Synchronize on the returned operation, terminal output, revision, or process exit; do not add arbitrary sleeps to make a test pass.

### Real GUI control

`ui key` dispatches a keystroke through the focused GPUI action tree. `ui click` dispatches a control-API click at a window-local point, `ui wheel` sends a wheel event, `ui state` reports the current window state, and `ui screenshot` requests a window render when the binary was built with `runtime-screenshot` and the platform renderer implements it.

```sh
target/debug/water ctl --socket /tmp/water-dev.sock ui key cmd-t
target/debug/water ctl --socket /tmp/water-dev.sock ui click \
  --x 240 --y 100 --click-count 2
target/debug/water ctl --socket /tmp/water-dev.sock ui state
target/debug/water ctl --socket /tmp/water-dev.sock ui screenshot \
  --output /tmp/water-window.png
```

Use Water's control API for GUI checks. Do not use `xdotool`, `xte`, AppleScript keystrokes, or native desktop coordinate injection. The control screenshot is a Water-window capture, not a desktop-wide screenshot; platform support must be verified on the machine running the check.

### Scenarios

```sh
target/debug/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/workspace_basic.json
target/debug/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/terminal_basic.json
target/debug/water ctl --socket /tmp/water-dev.sock scenario run \
  tests/scenarios/terminal_zsh.json
target/debug/water ctl --socket /tmp/water-dev.sock scenario run \
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
# Dev identity, optimized dev profile.
cargo build

# Release identity.
cargo build --release

# Assemble the macOS app; dev is the default variant.
bash scripts/build-macos-app.sh

# Assemble a release app explicitly.
WATER_APP_VARIANT=release bash scripts/build-macos-app.sh

# Refresh embedded server payloads only.
WATER_APP_VARIANT=dev bash scripts/build-embedded-servers.sh
```

The macOS script builds the GUI, `water-server`/`water-srv-dev`, and the four embedded server targets. Linux cross-builds use the repository's Zig linker and framework stubs. The app bundle must contain payloads whose variant marker matches the GUI.

## Tests and validation

```sh
cargo fmt --all -- --check
cargo clippy --all-targets -- -D warnings
cargo test --all-targets
cargo test --test ctl_smoke --test protocol --test resize_stream
```

For a real GUI smoke run, start the current binary with a unique socket/config and exercise `ui key`, `pane input`, `pane content`, and `ui state` through `water ctl`. Record the GUI/server PID and clean it up after the run. Cross-build and headless tests do not replace a native GUI check.

## macOS signing handoff

Signing is two-stage. The build machine creates a real unsigned archive; the GitHub Action only downloads, signs, verifies, and publishes it.

```sh
# Release: push the vVERSION tag first.
VERSION="$(awk -F ' *= *' '/^version = / { gsub(/"/, "", $2); print $2; exit }' Cargo.toml)"
WATER_APP_VARIANT=release CODESIGN_SKIP=1 \
  bash scripts/build-macos-app.sh
WATER_APP_VARIANT=release WATER_RELEASE_TAG="v${VERSION}" \
  WATER_RELEASE_PUBLICATION=release bash scripts/publish-unsigned-macos.sh

# Dev: use an already pushed dev-* tag.
DEV_TAG="dev-$(date -u +%Y%m%d-%H%M)"
WATER_APP_VARIANT=dev CODESIGN_SKIP=1 \
  bash scripts/build-macos-app.sh
WATER_APP_VARIANT=dev WATER_RELEASE_TAG="$DEV_TAG" \
  WATER_RELEASE_PUBLICATION=prerelease bash scripts/publish-unsigned-macos.sh
```

`publish-unsigned-macos.sh` requires the exact variant archive and a draft release, then dispatches `macos-signed.yml` with `variant`, `publication`, `tag`, `source_release`, and `source_asset`. The signing Action runs on `macos-14`; it does not compile Rust. Keep signing materials outside the repository and provide them only through the documented GitHub secrets.
