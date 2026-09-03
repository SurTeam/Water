# Water agent notes

- Phase order is strict: finish and validate one phase before starting the next. Phases 1 and 2 are accepted; the current target is Phase 3 terminal UX/performance. Normal GUI startup opens one real-shell terminal; new tabs and split panes must also open real-shell terminals. GUI shortcuts are `Cmd-\\` for a right-side split, `Cmd--` for a downward split, and `Cmd-T` for a new terminal tab. Use `--no-initial-terminal` for a workspace-without-terminal baseline or `--empty-workspace` for workspace-creation smoke scenarios. Do not add file browser or image behavior. Coding-agent work is limited to read-only detection and the sidebar Agents section (the binding contract in `ARCHITECTURE.md`); do not add structured Agent Surfaces until Phase 5.
- State mutation has one path: `AppCommand` -> `CommandDispatcher` -> model. UI, `waterctl`, and scenarios use that path; they must not mutate model fields directly.
- IDs are typed, stable newtypes. Never use a `Vec` index as identity.
- `CommandDispatcher` owns the model on the model thread. Control requests cross a channel; do not introduce a global `Mutex<ApplicationModel>`.
- Operations are observable and waitable. Tests and scenarios synchronize with operation completion or explicit wait primitives, never fixed sleeps.
- Keep GPUI pinned to the revision recorded in `ARCHITECTURE.md` and `Cargo.toml`; do not upgrade it mid-phase.
- UI is a projection of revisioned snapshots. Keep blocking I/O and model mutation off the GPUI thread; UI command handlers dispatch through a detached GPUI task and background executor.
- Terminal mutable state belongs to a dedicated PTY worker. The model stores terminal metadata and revisioned visible snapshots; `TerminalRegistry` is a narrow synchronized query/wait surface, not application-state ownership.
- Real-shell validation uses the detected zsh, preferring `/opt/homebrew/bin/zsh`; `tests/scenarios/terminal_zsh.json` runs zsh interactively with `-f` and waits on output/process state.
- Application defaults live separately in `AppConfig` and are loaded from `~/Library/Application Support/water/config.json` on macOS, or from `WATER_CONFIG`/`--config`. Keep feature toggles, theme colors, font metrics, and terminal scrollback limits out of hard-coded UI/worker paths.
- Default logs are structured and quiet. Use `RUST_LOG=water::pty=debug` (or another module) for focused diagnostics; never add per-cell/per-frame logs by default.
- Testing should definitely not take a long time. Aside from the compilation process, all other tests should be completed within one minute. If they take longer than that, we need to investigate the cause and fix the issue.
