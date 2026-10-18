# TUI 终端界面

实现入口：`internal/gotui/` 与 `cmd/water/`；共享模型/传输规则见 [服务与连接](remote-connections.md)，终端规则见 [终端管线](terminal-rendering.md)。

本文记录当前实现与限制；开发讨论先留在上下文，用户确认实现并要求 commit 后再按最终代码更新，见 [工作约定](../AGENTS.md#文档更新时机)。

Run `water tui` to connect to the same server-backed workspaces used by the GUI. The frontend uses the configured shell for new tabs and splits. A missing local server starts automatically when `server.auto_start` permits it. Closing the frontend detaches; shells and the server stay running.

```sh
water tui
water tui --socket /tmp/my-water.sock --config /path/to/config.json
water tui --ssh user@host
```

`WATER_CONTROL_SOCKET` and `WATER_CONFIG` supply the default socket and configuration. Dev and release use their existing separate server namespaces and compatibility checks. An incompatible or unresponsive listener is left intact. SSH uses Water's existing remote server discovery, compatibility checks, and forwarded Unix sockets.

The normal layout has a workspace sidebar, tab strip, and recursive split panes. Below 90 columns, the sidebar disappears, the top bar shows Workspace and Tabs selection buttons, and the selected pane fills the terminal without a separate title row. The Tabs button shows the current tab name, shortened to leave the Commands button fully visible. Very narrow terminals use `[W][T] [C]`. Click either button to open its selection popup; Tabs lists tabs in the current workspace. Tab names follow the GUI: a custom name takes priority, followed by the active pane’s agent or process name, then the default title. Resize the outer terminal to switch layouts. Narrow mode selects from the complete pane tree, so deeply nested panes remain available even in a short terminal. Click Commands at the top right to open the full command menu; the normal bottom bar is hidden to give the terminal another row. Confirmation and other input prompts still appear at the bottom, as do status messages. The status line is informational and does not open menus. Copy feedback disappears automatically after a brief flash. In wide mode, use Commands to open the menu; select **Select pane**, **Select workspace**, or **Select tab** to choose an item. Arrow keys and Enter also navigate menus. Long menus scroll with the keyboard selection.

Press **Ctrl-B**, release it, then press a command key:

| Key | Action |
| --- | --- |
| `?` or Space | Open menu |
| `p`, `s`, `T` | Select pane, workspace, or tab |
| `n`, `w` | Create tab or workspace |
| `%`, `"` | Split right or down |
| `o` | Next pane, including hidden panes in narrow mode |
| `[`, `]` | Previous or next tab |
| `{`, `}` | Previous or next workspace |
| `x`, `t` | Close pane or tab; type `yes` to confirm |
| `W` | Close workspace; type `yes` to confirm when it contains any tabs |
| `r`, `R` | Rename tab or workspace |
| `b` | Return to the bottom of terminal history |
| `:` | Dispatch a server command as JSON |
| `q` | Detach and restore the outer terminal |
| `Q` | Exit TUI and shut down the connected server, stopping all its terminals; type `yes` to confirm |
| Ctrl-B | Send a literal Ctrl-B to the child application |

The shared `shortcuts.close_workspace` setting also closes the selected workspace directly (default **Ctrl-Alt-W**), while **Ctrl-B W** remains available. Empty workspaces close immediately; a workspace containing any tabs requires `yes`, and Escape cancels. Confirmation stays bound to the workspace selected when the prompt opened. The TUI reads this binding at startup; use terminal-supported combinations such as `ctrl-alt-w` or `alt-shift-w` (Cmd/Super combinations are unavailable in ordinary terminals).

The JSON command prompt supports existing server operations beyond the shortcuts. Entity IDs are full UUID strings, available through `water ctl state`. For example:

```json
{"type":"pane.promote_to_tab","pane_id":"PANE_UUID"}
{"type":"pane.move_to_workspace","pane_id":"PANE_UUID","workspace_id":"WORKSPACE_UUID"}
{"type":"agents.inspect"}
```

The focused client owns the shared PTY grid. Outer-terminal focus reports update that ownership; on terminals without focus reports, actual keyboard or mouse activity can reclaim it. Remote resize events update observed emulator geometry without triggering repeated resize requests. The TUI reasserts its viewport after a layout change or renewed focus.

Click a pane to focus it. Drag split dividers to resize them. Application mouse tracking receives coordinates relative to the child terminal, excluding sidebar, tabs, and pane title. Touch terminals that translate taps into standard terminal mouse press/release reports use the same path. Wheel events go to applications that request mouse reporting; otherwise they scroll local history. Hold Shift to use local history scrolling or text selection even when an application requests mouse reporting. Dragging local text copies it through the outer terminal's clipboard protocol when supported. Paste uses bracketed paste when requested by the child application.

Terminal state comes from bounded server replay followed by ordered live output. The client suppresses historical terminal query replies, discards overlapping sequence numbers, and reattaches on a gap. Scrollback and terminal emulation belong to the frontend. Only visible panes attach to terminal output; selecting a hidden pane reconstructs it from available replay. History older than the server's bounded replay is not restored after detach or hiding a pane.

Gap reattach only repairs terminal sequence continuity within an existing
connection. If the server session or SSH transport disconnects, the event loop
returns server disconnected and exits; there is no GUI-style reconnect loop.
Run water tui again to attach to any sessions still retained by the server.

The TUI interface uses a fixed grayscale theme defined in `internal/gotui/render.go`: dark sidebar and status bar, separate gray tab and pane headers, brighter selected items, and visible split/sidebar rules. It does not read the GUI theme. The main canvas and default terminal cells use the outer terminal’s default background, matching the shell; applications retain their explicit ANSI foreground and background colors.

The frontend renders text, colors, cell styles, wide characters, cursor position, and alternate screens. Terminal graphics/images, desktop notifications, GUI screenshots/automation, and GUI settings dialogs are not rendered. Pixel mouse reports use terminal cell coordinates because the outer terminal does not provide reliable per-cell pixel geometry. SSH connections are selected at launch; each TUI instance has one server connection. Configuration changes continue to use the existing configuration file or GUI. Signals handled during the event loop restore terminal modes; a forced kill cannot run cleanup.

The automatically started server runs as a separate process using the same executable's internal `tui --serve` entry. Each startup writes diagnostics to a private temporary `water-<variant>-tui-server-*.log` file; a startup failure reports its path. Use `water ctl server shutdown` with the same socket when you intentionally want to stop the server and its shells.

To run the isolated real-PTY smoke check after building the binary:

```sh
python3 scripts/tui-smoke.py --binary /path/to/water
```

The check records a unique evidence directory, owned process IDs, socket/config paths, raw terminal output, and cleanup results. It checks automatic server startup, live output, split creation, the narrow selector, persistent state after detach, confirmed server shutdown with clean TUI exit, and terminal alternate-screen/mouse restoration. Focused Go tests additionally exercise the terminal decoder and exact application mouse coordinates.
