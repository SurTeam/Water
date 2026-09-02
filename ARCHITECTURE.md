# Water architecture

Status: **Phase 3 in progress**. Phases 1 and 2 are complete and validated; this document records the boundaries that must remain stable while terminal UX and performance evolve. File browser, preview, and agent behavior remain intentionally out of scope.

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
| Window | `App::open_window(WindowOptions, build_root_view)` | one native window |
| Entity/view | `AppContext::new` -> `Entity<T>` | `WorkspaceView` root entity |
| Render | `Render::render(&mut self, &mut Window, &mut Context<Self>)` | projection of `ModelSnapshot` |
| Input/focus | `MouseDownEvent`, `MouseMoveEvent`, `MouseUpEvent`, `KeyDownEvent`, `cx.listener`, `FocusHandle`/`Focusable` APIs | terminal focus, keyboard PTY routing, split shortcuts, selection, and mouse reporting |
| Async/background | `App::spawn`, `AsyncApp`, `background_executor()` | snapshot listener blocks only off the UI thread |
| Layout/text | `div`, flex/layout methods, `SharedString`, element children | tab bar and recursive pane projection |
| Testing | `#[gpui::test]`, `TestAppContext::add_window`, `run_until_parked` | focused terminal keyboard and split-shortcut view tests; domain/control tests remain headless |

The current upstream examples explicitly use `AppContext` for `cx.new`, `cx.open_window`, `cx.activate(true)`, and `cx.listener`/mouse event handlers. We do not rely on a crates.io version that can drift from the pinned examples.

## Ownership and data flow

Phase 2 adds a dedicated terminal runtime without changing the single application mutation path. A terminal worker owns the mutable `alacritty_terminal::Term` and PTY; the model owns serializable terminal metadata and receives revision notifications. A narrow `TerminalRegistry` publishes bounded visible snapshots and wait predicates. Application defaults are loaded separately through `AppConfig`; terminal workers receive the configured scrollback limit while GPUI receives the feature and theme projection.

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
                         GPUI WorkspaceView
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
                       args, status, columns, lines, last_output_revision }
```

`TerminalSnapshot` is a bounded, row-major visible projection containing cell
characters, ANSI colors, flags (bold/italic/underline/inverse/strike/wide),
cursor state, display offset, process state, and a monotonic terminal revision.
Scrollback remains inside the worker's `Term` grid. The normal per-terminal history is configured through the application defaults file (`1..=10,000` lines, default `10,000`). While the user is scrolled away from live output, the worker temporarily expands that grid within the aggregate `max_total_scrollback_lines` budget (default `100,000`) so new output cannot move the pinned viewport. Returning to the live end or sending keyboard input restores the normal limit and releases the temporary rows. Terminal dimensions are clamped to 2–512 columns and 1–256 lines to keep snapshots and control frames bounded.

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

Terminal worker notifications are applied by `CommandDispatcher::pump_background_events` on the model thread. Foreground process name and cwd metadata are refreshed asynchronously by each PTY worker; helper-process probes are deferred while a terminal is actively streaming output so metadata tracking does not compete with PTY throughput. Automatic tab titles follow the active pane until `title_override` is set by an explicit rename. A terminal exit emits `TerminalExited` and automatically removes its pane; if the pane is the tab's last leaf, the tab is removed as well. Closing a pane, tab, or workspace sends a worker shutdown that terminates the positive foreground process group without signaling Water itself. The terminal worker is retired while a bounded completed snapshot remains available to exit/output waiters, preventing cleanup from racing a query. No UI callback may push into `tabs`, rewrite a pane tree, replace a surface, or touch a `Term` directly.

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

`WorkspaceView` holds a `ModelSnapshot`, not the mutable model. It renders an always-visible, collapsible left workspace sidebar with ordered workspace rows, nested tab rows, activation, creation, deletion, inline rename controls, and a draggable width handle. The main area renders the tab bar, recursively projects the pane tree using each split ratio, and projects visible terminal rows from the included active-workspace `TerminalSnapshot`. The workspace owns a focus handle; key events are translated to PTY bytes/VT sequences and enqueued through `CommandClient` without blocking the GPUI thread. Click-to-focus, keyboard PTY routing, Shift-PageUp/Down, mouse-wheel scroll (including application mouse reporting), configurable bracketed Cmd-V paste, drag selection/copy, and Cmd-C/Cmd-D control-byte shortcuts are supported for the current terminal surface. `Cmd-E` toggles the sidebar, `Cmd-T` creates a tab, `Cmd-Shift-N` creates a workspace, `Cmd-Shift-E`/`Cmd-Shift-T` begin workspace/tab renames, `Cmd-H/J/K/L` moves to the nearest pane in the requested spatial direction, and `Cmd-Shift-W` closes the focused pane. Selection maps against measured monospaced cell metrics, normalizes wide-character spacers, and paints each Unicode glyph as an all-or-nothing two-cell selection. Focused terminals use a solid cursor; inactive panes/windows use a hollow cursor. Button/mouse handlers that change model state start detached GPUI tasks; dispatch, operation wait, and snapshot fetch run on the background executor. No blocking filesystem, PTY, parser, or scrollback work is performed in the GPUI thread. The normal GUI startup creates one workspace and one detected real-zsh terminal; new tabs and split panes inherit the focused pane's cwd and use the same default shell automatically. `Cmd-\\` creates a right-side horizontal split and `Cmd--` creates a downward vertical split. `--no-initial-terminal` keeps the workspace but omits the initial tab/PTY, while `--empty-workspace` starts with no workspace for scenarios that exercise workspace creation. The current Phase 3 renderer uses measured font metrics, configurable theme colors, and simple color runs; row/cell batching and font shaping remain future optimization work.

The model thread publishes snapshots only after changes through a single-slot coalescing mailbox. The design leaves a GPUI async subscription path for external control updates; no timer-driven redraw loop is used for idle state. Scrollback reservations are coordinated by a small shared budget owned by terminal workers; it is not application-model state.

## Testing

There is one scenario format and one runner. Each command step is:

```text
dispatch -> wait_operation -> next step
```

The runner has only bounded wait primitives (`operation_complete`, `event`, `state_revision_at_least`, `app_idle`, `terminal_contains`, and `process_exit`). Terminal waits use a condition variable over the worker's published snapshot/output state; they do not sleep. Assertions inspect state dumps and terminal cells, not pixels, coordinates, or screenshots.

Unit tests cover model topology, command behavior, terminal parsing, ANSI cell attributes, and worker synchronization. Headless tests start with an empty workspace; normal GUI startup creates one Terminal tab, `--no-initial-terminal` preserves a workspace without a tab, and `--empty-workspace` is the clean live baseline for workspace-creation smoke tests. The Phase 1 smoke scenario exercises workspace creation, tab lifecycle, split/focus/resize/close, and tab switching; the Phase 2 scenarios exercise PTY output, real zsh input, replacement, and process waits; Phase 3 tests cover workspace lifecycle, metadata-driven titles, cwd inheritance, and explicit rename pinning through the same serialized command path used by `waterctl`.

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
- `cargo test --all-targets`: all 38 tests passed
- `cargo build --release --bins`: passed on `aarch64-apple-darwin`
- live `water --empty-workspace` + `waterctl scenario run tests/scenarios/workspace_basic.json`: passed
- live `water` + `waterctl scenario run tests/scenarios/terminal_basic.json`: passed
- live `water` + `waterctl scenario run tests/scenarios/terminal_zsh.json`: passed
- terminal integration tests verify `printf`, ANSI red cell attributes, model-host projection, output waits, and process-exit waits
- benchmark: terminal feed/render benchmarks remain deferred to Phase 3

The local Xcode installation required the `MetalToolchain` component for GPUI's macOS shader build; it was installed before the successful builds.

## Phase gates

- **Phase 1:** app shell, model, commands, operations, events, Unix control, `waterctl`, scenario runner, GPUI projection. Complete.
- **Phase 2:** `alacritty_terminal` worker and terminal surface. Complete: PTY spawn, real zsh integration (`/opt/homebrew/bin/zsh -l` when available for default terminals), input, output, resize, scroll, bounded waits, process status, ANSI cell projection, and live scenario validation.
- **Phase 3:** in progress. Keyboard/PTY routing, multi-workspace/sidebar UX, workspace/tab lifecycle, cwd inheritance, asynchronous process titles, spatial pane focus, process-group shutdown, paste, navigation, mouse-wheel scrolling, measured cell rendering, split-aware resize, wide-character selection, configurable theme/features, and basic selection/copy are implemented; dirty rows, row/background batching, font shaping, and benchmarks remain.
- **Phase 4:** file browser and image preview.
- **Phase 5:** structured Agent Surface adapters.

A phase is complete only after build, unit tests, scenario smoke, and architecture review pass. Do not start the next phase early.
