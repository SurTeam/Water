# Water architecture

Status: **Phase 3 in progress**. Phases 1 and 2 are complete and validated; this document records the boundaries that must remain stable while terminal UX and performance evolve. File browser and preview remain intentionally out of scope. Read-only coding-agent detection (foreground-process classification, sidebar Agents area, `agent.started`/`agent.stopped` events) landed as the Phase 5 groundwork; structured Agent Surface adapters themselves remain out of scope.

## GPUI investigation and pin

GPUI is pre-1.0 and its upstream API changes frequently. On 2026-09-01 the `main` branch of `zed-industries/zed` resolved to:

```text
ce48461eaadd16c65c31f835511ab96bd3b6e746
```

Water pins both `gpui` and `gpui_platform` to that exact Git revision in `Cargo.toml`.

The implementation was checked against the upstream sources/examples at that revision:

- GPUI README: <https://github.com/zed-industries/zed/blob/ce48461eaadd16c65c31f835511ab96bd3b6e746/crates/gpui/README.md>
- `hello_world`: <https://github.com/zed-industries/zed/blob/ce48461eaadd16c65c31f835511ab96bd3b6e746/crates/gpui/examples/hello_world.rs>
- context guide: <https://github.com/zed-industries/zed/blob/ce48461eaadd16c65c31f835511ab96bd3b6e746/crates/gpui/docs/contexts.md>
- application API: <https://github.com/zed-industries/zed/blob/ce48461eaadd16c65c31f835511ab96bd3b6e746/crates/gpui/src/app.rs>
- view API: <https://github.com/zed-industries/zed/blob/ce48461eaadd16c65c31f835511ab96bd3b6e746/crates/gpui/src/view.rs>

The verified startup shape is:

```rust
application().run(|cx: &mut App| {
    cx.open_window(WindowOptions { ..Default::default() }, |_, cx| {
        cx.new(|_| RootView { /* ... */ })
    }).unwrap();
    cx.activate(true);
});
```

A root view is an `Entity<T>` whose `T: Render`. GPUI provides `Context<T>` for entity mutation/notification, `App` for application services, and `App::spawn`/background executors for asynchronous work. Phase 1 keeps the model thread independent of GPUI; the view receives revisioned projections and only dispatches commands.

The API checklist verified at the pin is:

| Need | GPUI surface | Phase 1 use |
|---|---|---|
| Startup/platform | `gpui_platform::application().run` | macOS Metal application loop |
| Window | `App::open_window(WindowOptions, build_root_view)` | one or more native windows sharing the same model projection |
| Entity/view | `AppContext::new` -> `Entity<T>` | `WorkspaceView` root entity |
| Render | `Render::render(&mut self, &mut Window, &mut Context<Self>)` | projection of `ModelSnapshot` |
| Input/focus | `MouseDownEvent`, `MouseMoveEvent`, `MouseUpEvent`, `KeyDownEvent`, `cx.listener`, `FocusHandle`/`Focusable` APIs | terminal focus, keyboard PTY routing, split shortcuts, selection, and mouse reporting |
| Async/background | `App::spawn`, `AsyncApp`, `background_executor()` | the single model snapshot receiver blocks off the UI thread, then fans each latest projection out to weak window-view handles |
| Layout/text | `div`, flex/layout methods, `SharedString`, element children | tab bar and recursive pane projection |
| Testing | `#[gpui::test]`, `TestAppContext::add_window`, `run_until_parked` | focused terminal keyboard and split-shortcut view tests; domain/control tests remain headless |

The current upstream examples explicitly use `AppContext` for `cx.new`, `cx.open_window`, `cx.activate(true)`, and `cx.listener`/mouse event handlers. We do not rely on a crates.io version that can drift from the pinned examples.

`WaterApplication` owns native-window/menu lifecycle only. `Cmd-N` creates another `WorkspaceView`, while each view keeps its own focus, selection, IME, and layout transients. A GPUI-side latest-snapshot fan-out updates every live weak view handle; it does not own or mutate `ApplicationModel`. Window-local actions are installed on the focused `WorkspaceView` root so `Cmd-W`, `Cmd-M`, terminal-tab creation, and split actions receive the correct `Window` instead of depending on `App::active_window()`. `Cmd-Q` is both bound to a no-op focused action and consumed by an application keystroke interceptor. `QuitMode::Explicit` keeps the model and PTYs alive when the last window is hidden.

The control socket exposes `ui.keystroke` and `ui.snapshot` as running-application test interfaces. UI requests cross a dedicated channel to the GPUI thread; synthetic keys are dispatched through `Window::dispatch_keystroke`, not translated into direct model mutation. Pane input remains `TerminalCommand::SendText` through the normal dispatcher path, while `waterctl pane content` resolves the pane's terminal snapshot and returns an exact viewport cell range. The macOS bundle is assembled by `scripts/build-macos-app.sh` from the release binary, `assets/macos/Info.plist`, and the Water-owned `.icns` resource.

## Ownership and data flow

Phase 2 adds a dedicated terminal runtime without changing the single application mutation path. A terminal worker owns the mutable `alacritty_terminal::Term` and PTY; the model owns serializable terminal metadata and receives revision notifications. A narrow `TerminalRegistry` publishes bounded visible snapshots and wait predicates. Application defaults are loaded separately through `AppConfig`; `AppConfigOverrides` forms an explicit optional-field override layer over the built-in defaults. The effective config includes startup cwd/control-socket/window policy, shell program and argument vector, terminal dimensions/history/font metrics, feature toggles, UI base font size, every rendered theme color, sidebar/layout metrics, and all Water shortcuts. Terminal workers receive the configured launch/history/theme values while GPUI receives the UI projection. The built-in dark theme mirrors the Kitty `kitty_normal.conf` base colors (`#2c2c2c` background and `#e4e4e4` foreground), with Kitty green `#339966` as the active pane/tab UI accent; Kitty is not read at runtime. The native macOS config path remains `~/Library/Application Support/water/config.json`; an existing XDG-style `~/.config/water/config.json` is also accepted, and `WATER_CONFIG`/`--config` take precedence. The promoted built-in defaults use `~` for terminal cwd, a 16px terminal font, 14px UI text, and a 170px sidebar.

```text
                           external process
                     waterctl / test automation
                               │
                 length-prefixed versioned JSON
                               │
                               ▼
                        Unix control socket
                               │
                               ▼
                         CommandClient
                               │
                         std channel
                               │
                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Model thread                                                         │
│                                                                      │
│  CommandDispatcher                                                  │
│       │                                                              │
│       ▼                                                              │
│  ApplicationModel                                                    │
│       │                                                              │
│       ├── Workspaces (ordered by WorkspaceId)                       │
│       │     └── Workspace                                             │
│       │           └── Tab                                             │
│       │           └── PaneNode                                       │
│       │                 └── Pane                                     │
│       │                       └── SurfaceId                          │
│       │                                                              │
│       ├── Surface registry: SurfaceId -> SurfaceState                │
│       ├── Operation registry                                         │
│       ├── EventBus (sequence + state_revision)                       │
│       └── TerminalManager                                             │
│             ├── TerminalRegistry (bounded snapshots + waits)          │
│             └── PTY workers (`Term` + `vte` parser)                    │
│                                                                      │
│  revisioned ModelSnapshot ───────────────► GPUI projection            │
└──────────────────────────────────────────────────────────────────────┘
                               ▲
                               │ AppCommand
                               │
                 GPUI WaterApplication / WorkspaceViews
```

The model thread is the sole owner of mutable application state. The socket thread and GPUI thread use handles/channels; neither receives a mutable model reference. The operation registry uses small per-operation synchronization cells, not a global application-state mutex, and retains at most 4,096 completed operations.

## Domain model

All long-lived identity is a typed, serializable `u64` newtype allocated by the model. IDs are never derived from a vector position.

```text
WorkspaceId  TabId  PaneId  SurfaceId  TerminalId  SessionId  OperationId
```

Phase 2 uses all seven IDs. `TerminalId` identifies the PTY/terminal core and `SessionId` identifies the logical session metadata attached to its surface; neither is inferred from a registry index.

```text
ApplicationModel
├── workspaces: BTreeMap<WorkspaceId, Workspace>
├── active_workspace: Option<WorkspaceId>
├── tabs: BTreeMap<TabId, Tab>
├── panes: BTreeMap<PaneId, Pane>
├── surfaces: BTreeMap<SurfaceId, SurfaceState>
└── state_revision: u64

Workspace { id, title, tabs: Vec<TabId>, active_tab: Option<TabId> }
Tab       { id, title, title_override, root: PaneNode, active_pane: PaneId }
PaneNode  = Leaf(PaneId)
          | Split { axis, ratio, first, second }
Pane      { id, surface: SurfaceId }
Surface registry: SurfaceId -> SurfaceState
```

`PaneNode` describes topology. `Pane` owns no split information. `SurfaceState` is an enum extension point; Phase 2 adds `Terminal(TerminalSurfaceState)` without making pane/workspace code depend on terminal behavior.

```text
TerminalSurfaceState { terminal_id, session_id, program, title, process_name, cwd,
                       args, status, columns, lines, last_output_revision, agent }
```

`agent` is the canonical pane<->coding-agent binding: `Option<DetectedAgent>` where `DetectedAgent { kind: AgentKind, active }` is re-derived on the model thread from every foreground-process refresh (`process_name` plus the probed argv) against the closed registry in `agent/model.rs`. `active` reflects PTY output within the worker's activity window (the worker owns the flag; probes carry it through unchanged). When the agent exits to a shell, the next refresh clears the binding — the model never keeps a stale association. `StateDump::agents` is the flattened, stable-order projection of every binding with its full `(workspace_id, tab_id, pane_id, terminal_id)` path plus cwd/status, so the sidebar, `waterctl state.dump`, and future agent surfaces consume one derived contract instead of re-walking the pane tree. Activating an agent is exactly `pane.focus` on its `pane_id` (`focus_pane` re-activates the owning workspace and tab in the model); there is no second mutation path. `AgentStarted`/`AgentStopped` events accompany kind transitions (activity-only flips are silent), which is also where hook-driven per-agent states (waiting for input, tool running, ...) should attach later without changing consumers.

`TerminalSnapshot` is a bounded, row-major visible projection containing cell
characters, ANSI colors, flags (bold/italic/underline/inverse/strike/wide),
cursor state, display offset, process state, a monotonic terminal revision, and at most one nearest row on either side of the viewport for fractional scrolling.
Scrollback remains inside the worker's `Term` grid. The per-terminal history is configured through the application defaults file (`1..=10,000` rows, default `2,000`). Scrollback growth is coordinated by a global byte budget (`terminal.max_total_scrollback_bytes`, default 64 MiB), not a row count, because a row's heap cost scales with the terminal width. The focused terminal may hold the full per-terminal limit, and while it is scrolled away from live output the worker temporarily expands that grid within the remaining budget so new output cannot move the pinned viewport; returning to the live end or sending keyboard input restores the normal limit and releases the temporary rows. A terminal that loses focus is trimmed to `terminal.inactive_scrollback_lines` (default 500) and releases its reservation, so many long background tabs cannot exhaust memory (the tmux `history-limit` / herdr background-pane model). Focus transitions flow through `CommandDispatcher` into `TerminalManager::set_focused_terminal`, which sends an explicit `SetFocused` worker command; workers never poll mutable application state. Terminal dimensions are clamped to 2–512 columns and 1–256 lines to keep snapshots and control frames bounded. The registry retains exactly one `Arc<TerminalSnapshot>` per terminal, and a retired (closed) terminal's retained snapshot is compacted to its last 24 visible rows plus 8 KiB of recent output so the (16-entry) retirement window stays bounded.

When no system `alacritty` terminfo entry is installed, spawn prepends the repo-bundled `assets/terminfo` database to `TERMINFO_DIRS` (override location with `WATER_TERMINFO_DIR`) so child shells run with `TERM=alacritty`. The bundled entry keeps the standard `clear=\E[H\E[2J` on purpose: zle's Ctrl-L and `/usr/bin/clear` read the same capability, and Ctrl-L must only push the screen into scrollback and redisplay the prompt at the top (classic alacritty/xterm semantics, history preserved). The "`clear` also erases scrollback" behavior is separated at the shell layer instead: Water injects a tiny ZDOTDIR chain (under `~/Library/Application Support/water/zsh-integration`, regenerated at launch, opt out with `WATER_NO_SHELL_INTEGRATION=1`) that sources the user's own zsh startup files first and then defines `clear()` as `command clear` plus `printf '\E[3J'` when the user has not defined their own. Nested shells receive the real ZDOTDIR back, so the integration runs exactly once per interactive tab. `waterctl debug memory` reports `retained_scrollback_lines/bytes`, `registry_snapshot_bytes`, and the configured limits; note that macOS's allocator may keep freed grid pages in reusable zones (`ps` RSS lags the accounting), so those numbers, not RSS, are the leak-free invariant.

## Mutation path

```text
AppCommand (serde)
       │
       ├── GPUI handler
       ├── waterctl
       └── ScenarioRunner
              │
              ▼
       CommandDispatcher::dispatch
              │
              ├── Operation: Pending -> Running -> Succeeded/Failed
              ├── model mutation
              ├── state_revision increment for each observable change
              ├── AppEvent with strictly increasing sequence
              └── revisioned snapshot notification
```

`get_operation(OperationId)` returns an immutable snapshot. `wait_operation(OperationId)` waits on operation completion rather than sleeping. Domain commands execute synchronously on the model thread; PTY input/output and ANSI parsing execute on terminal workers, with worker state published through bounded snapshots and condition-variable waits.

The command family is intentionally explicit:

- `WorkspaceCommand`: create/new, ensure, activate, rename, and delete/close workspaces
- `TabCommand`: new (with an automatic terminal), rename, close, and activate tabs
- `PaneCommand`: split (with an automatic terminal), close, spatial focus, and resize
- `SurfaceCommand`: replace an empty surface (future surface kinds return a typed availability error)
- `TerminalCommand`: spawn detected real zsh (`/opt/homebrew/bin/zsh`, Intel Homebrew, or `/bin/zsh`) with `-l` by default, or an explicit program, send text/raw bytes, resize, and scroll

Terminal worker notifications are applied by `CommandDispatcher::pump_background_events` on the model thread. PTY reads and queued worker commands use bounded per-turn budgets so sustained output or input cannot starve snapshot publication or the opposite direction of traffic. The registry keeps the latest snapshot and coalesces each terminal's output notification until the model atomically takes it. Foreground process name, cwd, and bounded argv probes run on one shared metadata thread and return through per-terminal result channels that wake the PTY poller; macOS uses `proc_name`/`proc_pidinfo`/`KERN_PROCARGS2`, Linux uses `/proc`; new probes are deferred while a terminal is actively streaming output so metadata tracking does not compete with PTY throughput. The worker additionally owns a bounded busy/idle activity flag (PTY output within the last two seconds) that rides the same `ProcessChanged` path; it is the `active` input to the agent binding below. Automatic tab titles follow the active pane until `title_override` is set by an explicit rename. A terminal exit emits `TerminalExited` and automatically removes its pane; if the pane is the tab's last leaf, the tab is removed as well. Closing a pane, tab, or workspace sends a worker shutdown that terminates the positive foreground process group without signaling Water itself. The terminal worker is retired while a bounded completed snapshot remains available to exit/output waiters, preventing cleanup from racing a query. No UI callback may push into `tabs`, rewrite a pane tree, replace a surface, or touch a `Term` directly.

## Events and revisions

`EventBus` assigns a monotonic `sequence`. Every observable mutation increments `ApplicationModel::state_revision` before emitting its `AppEvent`. The bounded event history supports debugging and automation without unbounded memory growth. Events are data, not UI callbacks.

## Control API

Phase 1 uses a Unix domain socket on macOS/Linux:

```sh
water --control-socket /tmp/water.sock
```

Messages are one length-prefixed JSON frame per request/response. Every request and response includes `protocol_version: 1` and `request_id`. The method surface is small and versionable:

- `command.dispatch`
- `operation.get`
- `operation.wait`
- `state.dump`
- `event.list`
- `debug.memory`
- `terminal.contains`
- `terminal.wait_exit`
- `terminal.snapshot`
- `ping`

Windows will get a named-pipe transport later; domain commands and the scenario runner do not depend on the transport.

## GPUI projection

`WorkspaceView` holds a `ModelSnapshot`, not the mutable model. It renders an app-owned integrated titlebar with compact horizontally scrollable tabs, custom window controls, a `◧` left-sidebar toggle, a reserved `◨` right-sidebar placeholder, drag handling, and double-click zoom toggling through the same native action as the maximize control; the workspace-only left sidebar is one continuously scrollable workspace-group list whose disclosure rows nest the state dump's agent bindings (busy/idle/agent-color dot, display label, project tail); clicking an agent row selects that workspace locally and dispatches `pane.focus` so the owning workspace and tab activate together, and a running agent can be renamed through the typed command path. Typed-ID in-window context menus handle workspace/tab rename and close actions, while a reusable in-window confirmation dialog protects closing a workspace that still contains tabs. Water uses a transparent native titlebar (gpui only grants the AppKit resizable/closable style masks through an explicit titlebar) whose native traffic lights are hidden through AppKit `setHidden` right after creation (gpui only supports repositioning them, and negative positions are an undefined-geometry hack), window_min_size sourced from startup.window_min_width/window_min_height, so windows resize freely down to the configured floor while Water's own controls stay visible. The Water menu ends with a Quit item that deliberately carries no key equivalent (cmd-q remains swallowed by the ignore-quit shortcut). The titlebar owns the tab strip; each native window keeps a local selected workspace and the main area recursively projects that workspace's pane tree using each split ratio. State snapshots use a split projection: every pane leaf in every workspace carries a cell-free `TerminalProjection` summary, while only the displayed tab of each workspace additionally shares the registry's single `Arc<TerminalSnapshot>` (never a copy), so separate windows can operate on different workspaces while hidden tabs never duplicate grid cells into the UI; full grids for hidden terminals are fetched one at a time through the registry-backed `terminal.snapshot` RPC. Only the focused active window negotiates a shared terminal's PTY size. The workspace owns a focus handle; key events are translated to PTY bytes/VT sequences and enqueued through `CommandClient` without blocking the GPUI thread. Click-to-focus, keyboard PTY routing, Shift-PageUp/Down, pixel and line mouse-wheel scrolling (including application mouse reporting), configurable bracketed Cmd-V paste, drag selection/copy, and Cmd-C/Cmd-D control-byte shortcuts are supported for the current terminal surface. Every window action and terminal navigation shortcut is sourced from `AppConfig.shortcuts`; the defaults include Cmd-E, Cmd-T, Cmd-Shift-N, Cmd-Shift-E/Cmd-Shift-T, Cmd-1..Cmd-0, Cmd-[/], Ctrl-Tab/Ctrl-Shift-Tab, Cmd-H/J/K/L, Cmd-Shift-W, Cmd-V, Cmd-C, and Cmd-D. Cmd-, and the Water menu open one reusable standalone Settings window. The settings editor exposes the complete effective config, validates colors/numbers/keystrokes, writes JSON off the GPUI thread, and fans immediate theme/UI-font/terminal-font/layout/feature/shortcut changes to all workspace views. A Restart Water control persists pending edits before requesting a process restart. Startup, shell, default-terminal-size, and PTY-history changes are retained as restart-required settings rather than mutating live workers. Configured shortcut values drive the corresponding window, workspace, pane, terminal, and scrolling actions without adding a second mutation path. Sidebar workspace/agent drags keep pending sources, group-bound geometry, drop previews, and edge auto-scroll in `WorkspaceView`; valid drops dispatch `WorkspaceCommand::Reorder` or `PaneCommand::MoveToWorkspace`, while split divider previews commit exactly one path-addressed `PaneCommand::ResizeSplit` (tab id plus a first/second descent path, so an ancestor split is committed by the divider the user actually dragged instead of the nearest inner split) on mouse-up, and a tab switch mid-drag cancels the commit. Blank main/sidebar backgrounds start native window movement only after child interactive handlers stop propagation. Selection maps against measured monospaced cell metrics, normalizes wide-character spacers, and paints each Unicode glyph as an all-or-nothing two-cell selection. Focused terminals use a solid cursor; inactive panes/windows use a hollow cursor. Button/mouse handlers that change model state start detached GPUI tasks; dispatch, operation wait, snapshot fetch, and config file I/O run off the GPUI thread. No blocking filesystem, PTY, parser, or scrollback work is performed in the GPUI thread. The normal GUI startup creates one workspace and one detected real-zsh terminal; `WorkspaceCommand::New` creates a user-facing workspace with one configured terminal tab, while `WorkspaceCommand::Create` remains the empty-workspace primitive used by startup flags. New tabs and split panes inherit the focused pane's cwd and use the configured default shell automatically. `Cmd-\\` creates a right-side horizontal split and `Cmd--` creates a downward vertical split. `--no-initial-terminal` keeps the workspace but omits the initial tab/PTY, while `--empty-workspace` starts with no workspace for scenarios that exercise workspace creation. The current Phase 3 renderer uses measured font metrics, configurable theme colors, and simple color runs; row/cell batching and font shaping remain future optimization work.

The model thread publishes snapshots only after changes through a single-slot coalescing mailbox. The design leaves a GPUI async subscription path for external control updates; no timer-driven redraw loop is used for idle state. Scrollback reservations are coordinated by a small shared budget owned by terminal workers; it is not application-model state.

## Testing

There is one scenario format and one runner. Each command step is:

```text
dispatch -> wait_operation -> next step
```

The runner has only bounded wait primitives (`operation_complete`, `event`, `state_revision_at_least`, `app_idle`, `terminal_contains`, and `process_exit`). Terminal waits use a condition variable over the worker's published snapshot/output state; they do not sleep. Assertions inspect state dumps and terminal cells, not pixels, coordinates, or screenshots.

Unit tests cover model topology, command behavior, terminal parsing, ANSI cell attributes, worker synchronization, config override merging, and the standalone settings shortcut/window. Headless tests start with an empty workspace; normal GUI startup creates one Terminal tab, `--no-initial-terminal` preserves a workspace without a tab, and `--empty-workspace` is the clean live baseline for workspace-creation smoke tests. The Phase 1 smoke scenario exercises workspace creation, tab lifecycle, split/focus/resize/close, and tab switching; the Phase 2 scenarios exercise PTY output, real zsh input, replacement, and process waits; Phase 3 tests cover workspace lifecycle, metadata-driven titles, cwd inheritance, explicit rename pinning, and config-driven terminal launch defaults through the same serialized command path used by `waterctl`.

## Threading rules by module

| Module | Owner | Mutation | Blocking work |
|---|---|---|---|
| `app/model` | model thread | command dispatcher only | none |
| `command` | model thread / operation cells | dispatch lifecycle | operation wait outside model loop |
| `control/server` | socket worker | enqueue commands | socket I/O only |
| `automation` | test/CLI caller | scenario backend | fixture/scenario file I/O only |
| `ui` | GPUI thread | view projection only | no blocking I/O |
| `terminal` | dedicated PTY worker | `Term`, parser, PTY I/O | all PTY/parser work off UI; registry waits may block only the caller/model request |

## Validation record

- `cargo fmt --all -- --check`: passed
- `cargo clippy --all-targets -- -D warnings`: passed
- `cargo test --all-targets`: all 93 tests passed
- `cargo build --release --bins`: passed on `aarch64-apple-darwin`
- live `water --empty-workspace` + `waterctl scenario run tests/scenarios/workspace_basic.json`: passed
- live `water` + `waterctl scenario run tests/scenarios/terminal_basic.json`: passed
- live `water` + `waterctl scenario run tests/scenarios/terminal_zsh.json`: passed
- live `water --empty-workspace` + `waterctl scenario run tests/scenarios/agent_detection.json`: passed
- live `water --empty-workspace` + `waterctl`: coding-agent detection smoke — a shell `exec -a` launching `codex`/`claude`/`sleep` produced the expected `state.dump.agents` bindings (cross-workspace/tab identity path, `pane.focus` jump, busy/idle flips as output paused, and binding removal when the agent exited to the shell)
- terminal integration tests verify `printf`, ANSI red cell attributes, model-host projection, output waits, and process-exit waits
- agent detection unit tests verify the argv/process-name registry, interpreter (`node`/`python3`) script-path matching, negative cases, the model binding/JSON back-compat path, and the dispatcher start/stop lifecycle events through a real foreground-process probe
- benchmark: terminal feed/render benchmarks remain deferred to Phase 3

The local Xcode installation required the `MetalToolchain` component for GPUI's macOS shader build; it was installed before the successful builds.

## Phase gates

- **Phase 1:** app shell, model, commands, operations, events, Unix control, `waterctl`, scenario runner, GPUI projection. Complete.
- **Phase 2:** `alacritty_terminal` worker and terminal surface. Complete: PTY spawn, real zsh integration (`/opt/homebrew/bin/zsh -l` when available for default terminals), input, output, resize, scroll, bounded waits, process status, ANSI cell projection, and live scenario validation.
- **Phase 3:** in progress. Keyboard/PTY routing, multi-workspace/sidebar UX, workspace/tab lifecycle, cwd inheritance, asynchronous process titles, spatial pane focus, process-group shutdown, paste, navigation, mouse-wheel scrolling, measured cell rendering, split-aware resize, wide-character selection, configurable theme/features, and basic selection/copy are implemented; dirty rows, row/background batching, font shaping, and benchmarks remain.
- **Phase 4:** file browser and image preview.
- **Phase 5:** structured Agent Surface adapters. Groundwork landed early inside Phase 3: read-only coding-agent detection with the canonical pane<->agent binding (`TerminalSurfaceState.agent`, `StateDump.agents`, `agent.started`/`agent.stopped`), the sidebar Agents section with click-to-jump, and a closed agent-kind registry. Future adapters (dedicated agent surfaces, hook-driven waiting/tool-running states, per-kind rendering) must extend this binding contract rather than re-detect in consumers.

A phase is complete only after build, unit tests, scenario smoke, and architecture review pass. Do not start the next phase early.
